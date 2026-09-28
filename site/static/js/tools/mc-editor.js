// The editor the colour codes and MOTD tools share: type text, and colour or
// format what's selected (or what you type next) from the toolbar, with the
// colours shown as you type. A transparent textarea sits over a mirror that
// draws each character in its style, so typing, selecting, copying and the
// caret stay the browser's own. Pasting § or & codes, or MiniMessage, reads
// them into styles. It keeps its own undo, since styles aren't the
// textarea's.
(function () {
  var t = window.pkTools;
  var mc = window.mcText;
  if (!t || !mc) return;

  // editor(root, opts): opts.lines caps the lines (0 for none), opts.bedrock()
  // says which edition's codes apply, opts.fallback is the colour the mirror
  // draws text without one in, opts.onChange(chars) hears every change.
  window.mcEditor = function (root, opts) {
    var input = root.querySelector('[data-input]');
    var mirror = root.querySelector('[data-mirror]');
    var hexField = root.querySelector('[data-hex]');
    var gradA = root.querySelector('[data-grad-a]');
    var gradB = root.querySelector('[data-grad-b]');
    var gradC = root.querySelector('[data-grad-c]');
    var gradients = new mc.Gradients();
    var chars = [];
    var pending = null;
    var history = [];
    var future = [];
    var bedrock = function () { return !!(opts.bedrock && opts.bedrock()); };

    function snapshot() {
      return { chars: chars.map(function (c) { return { ch: c.ch, st: mc.copyStyle(c.st) }; }), a: input.selectionStart, b: input.selectionEnd };
    }
    function remember() {
      history.push(snapshot());
      if (history.length > 100) history.shift();
      future = [];
    }
    function restore(s) {
      chars = s.chars;
      input.value = mc.text(chars);
      input.setSelectionRange(s.a, s.b);
      changed();
    }

    function styleAt(i) {
      if (pending) return pending;
      if (i > 0 && chars[i - 1]) return chars[i - 1].st;
      if (chars[i]) return chars[i].st;
      return mc.plain();
    }

    function drawMirror() {
      var frag = document.createDocumentFragment();
      chars.forEach(function (c) {
        if (c.ch === '\n') { frag.appendChild(document.createTextNode('\n')); return; }
        var span = document.createElement('span');
        span.textContent = c.ch;
        var color = c.st.c || opts.fallback;
        span.style.color = color;
        var dark = lightness(color) < 0.18;
        var shadows = [];
        if (c.st.b) shadows.push('1px 0 0 ' + color);
        if (dark) shadows.push('0 0 1px rgb(255 255 255 / 0.7)');
        if (shadows.length) span.style.textShadow = shadows.join(', ');
        var deco = [];
        if (c.st.u) deco.push('underline');
        if (c.st.s) deco.push('line-through');
        if (deco.length) span.style.textDecoration = deco.join(' ');
        if (c.st.i) span.style.fontStyle = 'italic';
        if (c.st.k) span.className = 'mc-obf';
        frag.appendChild(span);
      });
      // A last new line needs something after it, or the mirror is a line short.
      frag.appendChild(document.createTextNode('\u200b'));
      mirror.replaceChildren(frag);
      mirror.scrollTop = input.scrollTop;
    }
    function lightness(h) {
      var c = t.rgb(h);
      return (0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]) / 255;
    }

    function syncButtons() {
      var a = input.selectionStart;
      var b = input.selectionEnd;
      var range = a === b ? null : chars.slice(a, b);
      mc.FORMAT_KEYS.forEach(function (f) {
        var on = range ? range.length > 0 && range.every(function (c) { return c.st[f]; }) : !!styleAt(a)[f];
        root.querySelectorAll('[data-format="' + f + '"]').forEach(function (btn) { btn.setAttribute('aria-pressed', on ? 'true' : 'false'); });
      });
    }

    // The field grows with its text, up to where it scrolls instead.
    function fit() {
      input.style.height = 'auto';
      input.style.height = Math.min(input.scrollHeight, 360) + 'px';
      input.style.overflowY = input.scrollHeight > 360 ? 'auto' : 'hidden';
    }

    function changed() {
      gradients.apply(chars);
      drawMirror();
      fit();
      syncButtons();
      opts.onChange(chars);
    }

    function limit(list) {
      if (!opts.lines) return list;
      var lines = 0;
      return list.filter(function (c) {
        if (c.ch !== '\n') return true;
        lines++;
        return lines < opts.lines;
      });
    }

    // The textarea changed: find what was typed or removed, and give what
    // was typed the style before it, or the one picked for it.
    function sync() {
      var now = input.value;
      var old = mc.text(chars);
      if (now === old) return;
      var p = 0;
      while (p < now.length && p < old.length && now[p] === old[p]) p++;
      var s = 0;
      while (s < now.length - p && s < old.length - p && now[now.length - 1 - s] === old[old.length - 1 - s]) s++;
      var style = styleAt(p);
      var added = now.slice(p, now.length - s).split('').map(function (ch) { return { ch: ch, st: mc.copyStyle(style) }; });
      var caret = input.selectionStart;
      chars = limit(chars.slice(0, p).concat(added, chars.slice(old.length - s)));
      pending = null;
      // Past the cap on lines, the extra line breaks are dropped.
      if (mc.text(chars) !== now) {
        input.value = mc.text(chars);
        caret = Math.min(caret, chars.length);
        input.setSelectionRange(caret, caret);
      }
      changed();
    }

    function insert(list) {
      remember();
      var a = input.selectionStart;
      var b = input.selectionEnd;
      chars = limit(chars.slice(0, a).concat(list, chars.slice(b)));
      input.value = mc.text(chars);
      var caret = Math.min(a + list.length, chars.length);
      input.setSelectionRange(caret, caret);
      pending = null;
      changed();
    }

    // style changes the selection with fn(style), or with none selected,
    // the style of what's typed next.
    function style(fn) {
      var a = input.selectionStart;
      var b = input.selectionEnd;
      if (a === b) {
        pending = mc.copyStyle(styleAt(a));
        fn(pending, true);
        syncButtons();
        input.focus();
        return;
      }
      remember();
      for (var i = a; i < b; i++) fn(chars[i].st, false);
      changed();
      input.focus();
      input.setSelectionRange(a, b);
    }

    function color(code) {
      var c = mc.colorOf(code, bedrock());
      style(function (st) { st.c = c; st.l = code; st.g = 0; });
    }
    function hex(h) { style(function (st) { st.c = h; st.l = null; st.g = 0; }); }
    function format(f) {
      if (bedrock() && (f === 'u' || f === 's')) { t.toast('Bedrock has no underline or strikethrough.', 2400); return; }
      var a = input.selectionStart;
      var b = input.selectionEnd;
      var on = a === b ? !styleAt(a)[f] : !chars.slice(a, b).every(function (c) { return c.st[f]; });
      style(function (st) { st[f] = on; });
    }
    function gradient(stops) {
      var a = input.selectionStart;
      var b = input.selectionEnd;
      if (a === b) {
        // A gradient needs text to spread over: with nothing selected, it
        // goes over the whole line the caret is on.
        var text = mc.text(chars);
        a = text.lastIndexOf('\n', a - 1) + 1;
        b = text.indexOf('\n', a);
        if (b < 0) b = text.length;
        if (a === b) { t.toast('Type some text, then add the gradient to it.', 2400); return; }
        input.setSelectionRange(a, b);
      }
      var id = gradients.add(stops);
      style(function (st) { st.g = id; });
    }
    function clear() {
      var a = input.selectionStart;
      var b = input.selectionEnd;
      if (a === b) { input.setSelectionRange(0, chars.length); }
      style(function (st) { Object.assign(st, mc.plain()); });
    }

    // The toolbar keeps the textarea's selection: pressing a button with the
    // mouse doesn't move focus, and one reached with Tab uses the selection
    // the textarea keeps while it's out of focus.
    root.addEventListener('pointerdown', function (e) {
      if (e.target.closest('[data-color], [data-format], [data-clear], [data-apply-hex], [data-apply-gradient]')) e.preventDefault();
    });
    root.addEventListener('click', function (e) {
      var el = e.target.closest('button');
      if (!el || !root.contains(el)) return;
      if (el.hasAttribute('data-color')) color(el.getAttribute('data-color'));
      else if (el.hasAttribute('data-format')) format(el.getAttribute('data-format'));
      else if (el.hasAttribute('data-clear')) clear();
      else if (el.hasAttribute('data-apply-hex')) {
        var h = t.hex(hexField.value);
        if (!h) { hexField.setAttribute('aria-invalid', ''); hexField.focus(); return; }
        hexField.removeAttribute('aria-invalid');
        hexField.value = h;
        hex(h);
      } else if (el.hasAttribute('data-apply-gradient')) {
        var stops = [gradA, gradB, gradC].filter(Boolean).map(function (f) { return t.hex(f.value); });
        var bad = [gradA, gradB, gradC].filter(function (f, i) { return f && !stops[i] && (i < 2 || f.value.trim()); });
        [gradA, gradB, gradC].forEach(function (f) { if (f) f.toggleAttribute('aria-invalid', bad.indexOf(f) >= 0); });
        if (bad.length) { bad[0].focus(); return; }
        gradient(stops.filter(Boolean));
      }
    });
    [hexField, gradA, gradB, gradC].forEach(function (f) {
      if (!f) return;
      f.addEventListener('input', function () { f.removeAttribute('aria-invalid'); var swatch = root.querySelector('[data-now="' + f.getAttribute('data-hex-name') + '"]'); var h = t.hex(f.value); if (swatch && h) swatch.style.background = h; });
      f.addEventListener('keydown', function (e) {
        if (e.key !== 'Enter') return;
        e.preventDefault();
        var apply = f === hexField ? root.querySelector('[data-apply-hex]') : root.querySelector('[data-apply-gradient]');
        if (apply) apply.click();
      });
    });

    input.addEventListener('beforeinput', function (e) {
      if (e.inputType === 'historyUndo' || e.inputType === 'historyRedo') return;
      if (opts.lines && (e.inputType === 'insertLineBreak' || e.inputType === 'insertParagraph') && mc.text(chars).split('\n').length >= opts.lines) {
        e.preventDefault();
        return;
      }
      remember();
    });
    input.addEventListener('input', sync);
    input.addEventListener('paste', function (e) {
      var text = e.clipboardData && e.clipboardData.getData('text/plain');
      if (!text) return;
      var kind = mc.hasCodes(text, bedrock());
      if (!kind) return;
      e.preventDefault();
      var list = kind === 'legacy' ? mc.parseLegacy(text.replace(/\r\n?/g, '\n'), bedrock()) : mc.parseMiniMessage(text.replace(/\r\n?/g, '\n'), gradients);
      if (bedrock()) list.forEach(function (c) { c.st.u = false; c.st.s = false; });
      insert(list);
      t.toast(kind === 'legacy' ? 'Read the colour codes you pasted.' : 'Read the MiniMessage you pasted.', 2400);
    });
    input.addEventListener('keydown', function (e) {
      var mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      var k = e.key.toLowerCase();
      if (k === 'z' || k === 'y') {
        e.preventDefault();
        var redo = k === 'y' || e.shiftKey;
        var from = redo ? future : history;
        var to = redo ? history : future;
        if (!from.length) return;
        to.push(snapshot());
        restore(from.pop());
      } else if (k === 'b' || k === 'i' || k === 'u') {
        e.preventDefault();
        format({ b: 'b', i: 'i', u: 'u' }[k]);
      }
    });
    ['select', 'keyup', 'mouseup', 'focus'].forEach(function (ev) { input.addEventListener(ev, syncButtons); });
    input.addEventListener('keydown', function (e) { if (/^Arrow|Home|End|Page/.test(e.key)) pending = null; });
    input.addEventListener('mousedown', function () { pending = null; });
    input.addEventListener('scroll', function () { mirror.scrollTop = input.scrollTop; });
    window.addEventListener('resize', fit);

    return {
      gradients: gradients,
      chars: function () { return chars; },
      set: function (list) {
        chars = limit(list);
        input.value = mc.text(chars);
        history = [];
        future = [];
        pending = null;
        changed();
      },
      load: function (list) {
        remember();
        chars = limit(list);
        input.value = mc.text(chars);
        pending = null;
        changed();
      },
      refresh: changed,
      input: input,
    };
  };
})();

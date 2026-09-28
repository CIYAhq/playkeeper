// Minecraft text for the free tools (/tools/color-codes, /tools/motd): the
// colour and formatting codes of Java and Bedrock Edition, text as a list
// of styled characters, reading § and & codes and MiniMessage, gradients,
// the widths the game's font gives characters (to wrap a MOTD where the
// server list does), drawing it on the page, and writing it back out as &
// or § codes, a server.properties line, MiniMessage, JSON text or a MiniMOTD
// block. Colours are minecraft.wiki's (Formatting codes, 28 Sep 2026).
(function () {
  var t = window.pkTools;
  if (!t) return;

  // code: [name, MiniMessage and JSON name, Java colour, Bedrock colour]
  var COLORS = {
    0: ['Black', 'black', '#000000', '#000000'],
    1: ['Dark blue', 'dark_blue', '#0000aa', '#0000aa'],
    2: ['Dark green', 'dark_green', '#00aa00', '#00aa00'],
    3: ['Dark aqua', 'dark_aqua', '#00aaaa', '#00aaaa'],
    4: ['Dark red', 'dark_red', '#aa0000', '#aa0000'],
    5: ['Dark purple', 'dark_purple', '#aa00aa', '#aa00aa'],
    6: ['Gold', 'gold', '#ffaa00', '#ffaa00'],
    7: ['Gray', 'gray', '#aaaaaa', '#c5c5c5'],
    8: ['Dark gray', 'dark_gray', '#555555', '#545454'],
    9: ['Blue', 'blue', '#5555ff', '#447fff'],
    a: ['Green', 'green', '#55ff55', '#54ff54'],
    b: ['Aqua', 'aqua', '#55ffff', '#54ffff'],
    c: ['Red', 'red', '#ff5555', '#ff5454'],
    d: ['Light purple', 'light_purple', '#ff55ff', '#ff54ff'],
    e: ['Yellow', 'yellow', '#ffff55', '#ffff54'],
    f: ['White', 'white', '#ffffff', '#ffffff'],
  };
  // Bedrock Edition's own colours; m and n are formats on Java.
  var BEDROCK = {
    g: ['Minecoin gold', 'minecoin_gold', '#efce16'],
    h: ['Quartz', 'material_quartz', '#d9ccb8'],
    i: ['Iron', 'material_iron', '#a9b4b7'],
    j: ['Netherite', 'material_netherite', '#8f727d'],
    m: ['Redstone', 'material_redstone', '#ee222c'],
    n: ['Copper', 'material_copper', '#c87363'],
    p: ['Gold (material)', 'material_gold', '#ffbf1e'],
    q: ['Emerald', 'material_emerald', '#13a045'],
    s: ['Diamond', 'material_diamond', '#5fecff'],
    t: ['Lapis', 'material_lapis', '#577bff'],
    u: ['Amethyst', 'material_amethyst', '#b66cdd'],
    v: ['Resin', 'material_resin', '#ff6a00'],
    w: ['Party blue', 'party_blue', '#8bb3ff'],
  };
  var FORMATS = { k: 'k', l: 'b', m: 's', n: 'u', o: 'i' };
  var FORMAT_KEYS = ['b', 'i', 'u', 's', 'k'];
  var MM = { b: 'bold', i: 'italic', u: 'underlined', s: 'strikethrough', k: 'obfuscated' };
  var LEGACY = { b: 'l', i: 'o', u: 'n', s: 'm', k: 'k' };

  function colorOf(code, bedrock) {
    if (COLORS[code]) return COLORS[code][bedrock ? 3 : 2];
    if (bedrock && BEDROCK[code]) return BEDROCK[code][2];
    return null;
  }

  // A style: c the colour (#rrggbb, or null for the default), l the legacy
  // code it came from, if any, b i u s k the formats, g a gradient's id.
  function plain() { return { c: null, l: null, b: false, i: false, u: false, s: false, k: false, g: 0 }; }
  function copyStyle(s) { return { c: s.c, l: s.l, b: s.b, i: s.i, u: s.u, s: s.s, k: s.k, g: s.g }; }
  function sameStyle(a, b) { return a.c === b.c && a.b === b.b && a.i === b.i && a.u === b.u && a.s === b.s && a.k === b.k; }

  var isCode = function (ch, bedrock) { return /[0-9a-fk-or]/i.test(ch) || (bedrock && /[g-jpqs-w]/i.test(ch)); };

  // parseLegacy reads text with § or & codes into styled characters: hex as
  // &#rrggbb or &x&r&r&g&g&b&b too. A & not followed by a code is text, as
  // in "Tom & Jerry". On Java a colour ends the formats before it; on Bedrock
  // they carry on.
  function parseLegacy(text, bedrock) {
    var out = [];
    var st = plain();
    for (var i = 0; i < text.length; i++) {
      var ch = text[i];
      var next = text[i + 1];
      if ((ch === '§' || ch === '&') && next !== undefined) {
        var hexHash = /^#([0-9a-f]{6})/i.exec(text.slice(i + 1));
        if (hexHash) {
          st = bedrock ? Object.assign(copyStyle(st), { c: '#' + hexHash[1].toLowerCase(), l: null }) : Object.assign(plain(), { c: '#' + hexHash[1].toLowerCase() });
          i += 7;
          continue;
        }
        var hexX = new RegExp('^[xX]((?:[§&][0-9a-fA-F]){6})').exec(text.slice(i + 1));
        if (hexX) {
          st = Object.assign(plain(), { c: '#' + hexX[1].replace(/[§&]/g, '').toLowerCase() });
          i += 13;
          continue;
        }
        var code = next.toLowerCase();
        if (isCode(code, bedrock)) {
          if (code === 'r') st = plain();
          else if (!bedrock && FORMATS[code]) st[FORMATS[code]] = true;
          else if (bedrock && /[klo]/.test(code)) st[FORMATS[code]] = true;
          else {
            var c = colorOf(code, bedrock);
            if (c) st = bedrock ? Object.assign(copyStyle(st), { c: c, l: code, g: 0 }) : Object.assign(plain(), { c: c, l: code });
          }
          i++;
          continue;
        }
      }
      out.push({ ch: ch, st: copyStyle(st) });
    }
    return out;
  }

  var NAMED = {};
  Object.keys(COLORS).forEach(function (k) { NAMED[COLORS[k][1]] = k; });
  NAMED.grey = '7';
  NAMED.dark_grey = '8';
  var MM_FORMATS = { bold: 'b', b: 'b', italic: 'i', i: 'i', em: 'i', underlined: 'u', u: 'u', strikethrough: 's', st: 's', obfuscated: 'k', obf: 'k' };

  // parseMiniMessage reads the parts of MiniMessage a MOTD or chat line uses:
  // colours by name or hex, the five decorations, gradients, reset and new
  // lines. Other tags stay as text.
  function parseMiniMessage(text, gradients) {
    var out = [];
    var stack = [plain()];
    var grads = [];
    var re = /<(\/?)([#!]?[a-z0-9_:#.-]+)>|\\([<\\])|([\s\S])/gi;
    var m;
    while ((m = re.exec(text))) {
      var top = stack[stack.length - 1];
      if (m[4] !== undefined || m[3] !== undefined) {
        var ch = m[4] !== undefined ? m[4] : m[3];
        var st = copyStyle(top);
        if (grads.length) st.g = grads[grads.length - 1];
        out.push({ ch: ch, st: st });
        continue;
      }
      var closing = m[1] === '/';
      var tag = m[2].toLowerCase();
      var name = tag.split(':')[0];
      if (name === 'newline' || name === 'br') { out.push({ ch: '\n', st: copyStyle(top) }); continue; }
      if (closing) {
        if (name === 'gradient' || name === 'rainbow') grads.pop();
        else if (stack.length > 1) stack.pop();
        continue;
      }
      if (name === 'reset') { stack = [plain()]; grads = []; continue; }
      var next = copyStyle(top);
      if (MM_FORMATS[name] !== undefined) {
        next[MM_FORMATS[name]] = tag.split(':')[1] !== 'false';
      } else if (NAMED[name] !== undefined) {
        next.c = COLORS[NAMED[name]][2];
        next.l = NAMED[name];
      } else if (/^#[0-9a-f]{6}$/.test(name)) {
        next.c = name;
        next.l = null;
      } else if (name === 'color' || name === 'colour' || name === 'c') {
        var v = tag.split(':')[1] || '';
        if (NAMED[v] !== undefined) { next.c = COLORS[NAMED[v]][2]; next.l = NAMED[v]; } else if (/^#[0-9a-f]{6}$/.test(v)) { next.c = v; next.l = null; }
      } else if (name === 'gradient' || name === 'rainbow') {
        var stops = name === 'rainbow' ? ['#ff0000', '#ffff00', '#00ff00', '#00ffff', '#0000ff', '#ff00ff'] : tag.split(':').slice(1).map(function (s) { return NAMED[s] !== undefined ? COLORS[NAMED[s]][2] : t.hex(s); }).filter(Boolean);
        if (stops.length === 1) stops.push(stops[0]);
        if (stops.length < 2) stops = ['#ffffff', '#000000'];
        grads.push(gradients.add(stops));
        continue;
      } else {
        out.push.apply(out, Array.from(m[0]).map(function (ch) { return { ch: ch, st: copyStyle(top) }; }));
        continue;
      }
      stack.push(next);
    }
    return out;
  }

  // Whether pasted text holds codes worth reading.
  function hasCodes(text) {
    if (/[§&](?:[0-9a-fk-or]|#[0-9a-f]{6}|x[§&][0-9a-f])/i.test(text)) return 'legacy';
    if (/<(?:\/?(?:#[0-9a-f]{6}|color:|gradient|rainbow|bold|italic|underlined|strikethrough|obfuscated|reset|newline|b|i|u|st)|(?:black|dark_blue|dark_green|dark_aqua|dark_red|dark_purple|gold|gr[ae]y|dark_gr[ae]y|blue|green|aqua|red|light_purple|yellow|white))/i.test(text)) return 'minimessage';
    return null;
  }

  // Gradients: each set of stops has an id; the characters that carry it
  // are coloured by where they sit in their run, recomputed after each edit
  // the way MiniMessage spreads a gradient over its text.
  function Gradients() {
    this.list = {};
    this.next = 1;
  }
  Gradients.prototype.add = function (stops) {
    var id = this.next++;
    this.list[id] = stops.slice();
    return id;
  };
  Gradients.prototype.at = function (stops, f) {
    if (stops.length === 1) return stops[0];
    var span = f * (stops.length - 1);
    var k = Math.min(stops.length - 2, Math.floor(span));
    var a = t.rgb(stops[k]);
    var b = t.rgb(stops[k + 1]);
    var u = span - k;
    return t.toHex([a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u, a[2] + (b[2] - a[2]) * u]);
  };
  // apply colours each run of a gradient's characters, spaces and new
  // lines included, as MiniMessage does.
  Gradients.prototype.apply = function (chars) {
    var self = this;
    var i = 0;
    while (i < chars.length) {
      var g = chars[i].st.g;
      if (!g || !self.list[g]) { i++; continue; }
      var j = i;
      while (j < chars.length && chars[j].st.g === g) j++;
      var n = j - i;
      for (var k = i; k < j; k++) {
        chars[k].st.c = self.at(self.list[g], n === 1 ? 0 : (k - i) / (n - 1));
        chars[k].st.l = null;
      }
      i = j;
    }
  };

  // The width of each character in the game's default font, in its pixels,
  // before the one-pixel gap after it; bold adds a pixel. Characters not
  // listed are 5 wide.
  var WIDTH = { ' ': 3, '!': 1, '"': 3, "'": 1, '(': 4, ')': 4, ',': 1, '.': 1, ':': 1, ';': 1, '<': 4, '>': 4, '@': 6, I: 3, '[': 3, ']': 3, '`': 2, f: 4, i: 1, k: 4, l: 1, t: 4, '{': 4, '|': 1, '}': 4 };
  function advance(ch, bold) { return (WIDTH[ch] !== undefined ? WIDTH[ch] : 5) + 1 + (bold ? 1 : 0); }
  function widthOf(chars) { return chars.reduce(function (n, c) { return n + (c.ch === '\n' ? 0 : advance(c.ch, c.st.b)); }, 0); }

  // wrap splits text into lines no wider than max, at spaces where it can,
  // as the game's font does; a new line always breaks.
  function wrap(chars, max) {
    var lines = [];
    var line = [];
    var w = 0;
    var space = -1;
    chars.forEach(function (c) {
      if (c.ch === '\n') { lines.push(line); line = []; w = 0; space = -1; return; }
      var a = advance(c.ch, c.st.b);
      if (w + a > max && line.length) {
        if (space >= 0 && c.ch !== ' ') {
          lines.push(line.slice(0, space));
          line = line.slice(space + 1);
        } else {
          lines.push(line);
          line = [];
        }
        w = widthOf(line);
        space = -1;
        if (c.ch === ' ') return;
      }
      if (c.ch === ' ') space = line.length;
      line.push(c);
      w += a;
    });
    lines.push(line);
    return lines;
  }

  // The drop shadow the game draws under coloured text: its colour at a
  // quarter of the brightness, (rgb & 0xFCFCFC) >> 2.
  function shadowOf(h) {
    return t.toHex(t.rgb(h).map(function (v) { return (v & 0xfc) >> 2; }));
  }

  // Obfuscated characters change every frame to others of the same width.
  var BY_WIDTH = {};
  '!"\'(),.:;<>@I[]`fiklt{|}ABCDEFGHJKLMNOPQRSTUVWXYZabcdeghjmnopqrsuvwxyz0123456789#$%&*+-/=?^_~'.split('').forEach(function (ch) {
    var w = advance(ch, false);
    (BY_WIDTH[w] = BY_WIDTH[w] || []).push(ch);
  });
  function scramble(ch) {
    var list = BY_WIDTH[advance(ch, false)] || BY_WIDTH[6];
    return list[Math.floor(Math.random() * list.length)];
  }
  var obfuscated = [];
  var ticking = 0;
  function tick() {
    obfuscated = obfuscated.filter(function (el) { return el.isConnected; });
    if (!obfuscated.length || t.reduce.matches) { ticking = 0; return; }
    obfuscated.forEach(function (el) { if (el.textContent.trim()) el.textContent = scramble(el.getAttribute('data-ch')); });
    ticking = setTimeout(tick, 70);
  }

  // render draws styled characters into el as spans: fallback is the colour
  // of text without one, shadow whether to draw the game's drop shadow.
  function render(el, chars, fallback, shadow) {
    var frag = document.createDocumentFragment();
    var i = 0;
    while (i < chars.length) {
      var st = chars[i].st;
      var j = i;
      while (j < chars.length && sameStyle(chars[j].st, st) && chars[j].ch !== '\n' && chars[i].ch !== '\n') j++;
      if (chars[i].ch === '\n') { frag.appendChild(document.createElement('br')); i++; continue; }
      var color = st.c || fallback;
      var text = chars.slice(i, j).map(function (c) { return c.ch; }).join('');
      var parts = st.k ? Array.from(text) : [text];
      parts.forEach(function (p) {
        var span = document.createElement('span');
        span.textContent = st.k && p.trim() ? scramble(p) : p;
        span.style.color = color;
        if (shadow) span.style.textShadow = (st.b ? '1px 0 0 ' + color + ', ' : '') + '0.125em 0.125em 0 ' + shadowOf(color);
        else if (st.b) span.style.textShadow = '1px 0 0 ' + color;
        var deco = [];
        if (st.u) deco.push('underline');
        if (st.s) deco.push('line-through');
        if (deco.length) span.style.textDecoration = deco.join(' ');
        if (st.i) span.style.fontStyle = 'italic';
        if (st.k && p.trim()) {
          span.setAttribute('data-ch', p);
          obfuscated.push(span);
        }
        frag.appendChild(span);
      });
      i = j;
    }
    el.replaceChildren(frag);
    if (obfuscated.length && !ticking && !t.reduce.matches) ticking = setTimeout(tick, 70);
  }

  // ---------- Writing it out ----------

  // legacy writes & or § codes. hex is how a colour off the 16 is written:
  // 'hash' as &#rrggbb (EssentialsX and most plugins), 'x' as
  // §x§r§r§g§g§b§b (Spigot and Paper), or 'nearest' as the closest of the 16.
  function legacy(chars, sym, hex, bedrock) {
    var out = '';
    var cur = plain();
    // The colour as it's written: with 'nearest', letters of a gradient
    // that come out as the same one of the 16 share one code.
    var code = function (st) {
      if (!st.c) return null;
      if (st.l) return st.l;
      return hex === 'nearest' ? nearest(st.c, bedrock) : st.c;
    };
    var colorCode = function (st) {
      var k = code(st);
      if (!k) return sym + 'r';
      if (k.length === 1) return sym + k;
      if (hex === 'hash') return sym + k;
      return sym + 'x' + k.slice(1).split('').map(function (d) { return sym + d; }).join('');
    };
    chars.forEach(function (c) {
      var st = c.st;
      if (c.ch === '\n') { out += '\n'; return; }
      var lost = FORMAT_KEYS.some(function (f) { return cur[f] && !st[f]; });
      var colorChanged = code(st) !== code(cur);
      if (colorChanged || lost) {
        // A colour code ends the formats on Java; on Bedrock only a reset does.
        if (bedrock && lost) {
          out += sym + 'r';
          if (code(st)) out += colorCode(st);
        } else if (code(st) || code(cur) || lost) {
          out += colorCode(st);
        }
        cur = Object.assign(plain(), { c: st.c, l: st.l });
      }
      FORMAT_KEYS.forEach(function (f) { if (st[f] && !cur[f]) out += sym + LEGACY[f]; });
      cur = copyStyle(st);
      out += c.ch;
    });
    return out;
  }

  function nearest(h, bedrock) {
    var c = t.rgb(h);
    var best = 'f';
    var d = Infinity;
    Object.keys(COLORS).forEach(function (k) {
      var o = t.rgb(COLORS[k][bedrock ? 3 : 2]);
      var dd = Math.pow(c[0] - o[0], 2) * 0.3 + Math.pow(c[1] - o[1], 2) * 0.59 + Math.pow(c[2] - o[2], 2) * 0.11;
      if (dd < d) { d = dd; best = k; }
    });
    return best;
  }

  // properties writes a server.properties value: § codes, then everything
  // past ASCII and the § itself as \uXXXX, and a new line as \n, which every
  // server reads whatever the file's encoding. A value's leading spaces are
  // dropped when the file is read unless the first is written as "\ ".
  function properties(chars) {
    var text = legacy(chars, '§', 'nearest', false);
    return text.replace(/\\/g, '\\\\').replace(/\n/g, '\\n').replace(/[^\x20-\x7e]/g, function (ch) {
      return '\\u' + ('000' + ch.charCodeAt(0).toString(16).toUpperCase()).slice(-4);
    }).replace(/^ /, '\\ ');
  }

  // minimessage writes MiniMessage: named colours for the 16, hex for the
  // rest, and a gradient's run as one <gradient> tag.
  function minimessage(chars, gradients) {
    var out = '';
    var open = [];
    var esc = function (s) { return s.replace(/\\/g, '\\\\').replace(/</g, '\\<'); };
    var tagsOf = function (st) {
      var tags = [];
      if (st.g && gradients.list[st.g]) tags.push({ open: 'gradient:' + gradients.list[st.g].join(':'), close: 'gradient', key: 'g' + st.g });
      else if (st.c && st.l && COLORS[st.l]) tags.push({ open: COLORS[st.l][1], close: COLORS[st.l][1], key: 'c' + st.l });
      else if (st.c) tags.push({ open: 'color:' + st.c, close: 'color', key: 'c' + st.c });
      FORMAT_KEYS.forEach(function (f) { if (st[f]) tags.push({ open: MM[f], close: MM[f], key: f }); });
      return tags;
    };
    var i = 0;
    while (i < chars.length) {
      var c = chars[i];
      if (c.ch === '\n') { out += '<newline>'; i++; continue; }
      var want = tagsOf(c.st);
      var keep = 0;
      while (keep < open.length && keep < want.length && open[keep].key === want[keep].key) keep++;
      for (var k = open.length - 1; k >= keep; k--) out += '</' + open[k].close + '>';
      open = open.slice(0, keep);
      for (var n = keep; n < want.length; n++) { out += '<' + want[n].open + '>'; open.push(want[n]); }
      out += esc(c.ch);
      i++;
    }
    return out;
  }

  // json writes JSON text, as /tellraw and /title take it: a part for each
  // run of one style, after an empty one so styles don't carry across.
  function json(chars) {
    var parts = [''];
    var i = 0;
    while (i < chars.length) {
      var st = chars[i].st;
      var j = i;
      while (j < chars.length && sameStyle(chars[j].st, st)) j++;
      var part = { text: chars.slice(i, j).map(function (c) { return c.ch; }).join('') };
      if (st.c) part.color = st.l && COLORS[st.l] ? COLORS[st.l][1] : st.c;
      FORMAT_KEYS.forEach(function (f) { if (st[f]) part[MM[f]] = true; });
      parts.push(part);
      i = j;
    }
    return JSON.stringify(parts.length === 2 && typeof parts[1] === 'object' && Object.keys(parts[1]).length === 1 ? parts[1].text : parts);
  }

  // text is the characters without styles.
  function text(chars) { return chars.map(function (c) { return c.ch; }).join(''); }

  window.mcText = {
    COLORS: COLORS,
    BEDROCK: BEDROCK,
    colorOf: colorOf,
    plain: plain,
    copyStyle: copyStyle,
    sameStyle: sameStyle,
    parseLegacy: parseLegacy,
    parseMiniMessage: parseMiniMessage,
    hasCodes: hasCodes,
    Gradients: Gradients,
    advance: advance,
    widthOf: widthOf,
    wrap: wrap,
    shadowOf: shadowOf,
    render: render,
    legacy: legacy,
    nearest: nearest,
    properties: properties,
    minimessage: minimessage,
    json: json,
    text: text,
    FORMAT_KEYS: FORMAT_KEYS,
  };
})();

// The MOTD generator (/tools/motd): two lines in the editor, drawn in the
// server list as the game draws them (wrapped at 271 of its pixels, two
// lines shown, gray unless coloured), with how wide each line is, and
// written for server.properties, MiniMOTD, and as § or & codes.
(function () {
  var t = window.pkTools;
  var mc = window.mcText;
  var root = document.querySelector('[data-motd-tool]');
  if (!t || !mc || !window.mcEditor || !root) return;
  // The width the server list wraps a MOTD at, in the game's pixels: the
  // list's row (305) less the icon (32) and the gap beside it (2).
  var MAX = 271;
  var SPACE = mc.advance(' ', false);
  var motdEl = root.querySelector('[data-list-motd]');
  var nameEl = root.querySelector('[data-list-name]');
  var icon = root.querySelector('[data-list-icon]');
  var warning = root.querySelector('[data-warning]');
  var hexNote = root.querySelector('[data-note-hex]');
  var out = {
    props: root.querySelector('#motd-out-props'),
    mini: root.querySelector('#motd-out-mini'),
    sect: root.querySelector('#motd-out-sect'),
    amp: root.querySelector('#motd-out-amp'),
  };
  var editor = null;

  var PRESETS = {
    friends: '<green>Survival with friends<newline><gray>Always on. Say hi in chat!',
    gradient: '<gradient:#55ffff:#5555ff><bold>PIP LAND</bold></gradient> <dark_gray>| <white>Survival SMP<newline><yellow>New: <gray>a live map and weekly backups',
    modded: '<gold><bold>All the Mods 10</bold> <gray>on <aqua>NeoForge<newline><gray>Friends only, map pre-generated',
    maintenance: '<red><bold>Down for maintenance<newline><gray>Back in a few minutes. Thanks for waiting!',
  };

  // lines splits the text at its line break.
  function lines(chars) {
    var ls = [[]];
    chars.forEach(function (c) {
      if (c.ch === '\n') ls.push([]);
      else ls[ls.length - 1].push(c);
    });
    return ls.slice(0, 2);
  }
  function join(ls) {
    var outChars = [];
    ls.forEach(function (l, i) {
      if (i) outChars.push({ ch: '\n', st: mc.plain() });
      outChars.push.apply(outChars, l);
    });
    return outChars;
  }
  // Centred lines start with as many spaces as half the room left.
  function shaped(chars) {
    var ls = lines(chars);
    if (t.value(root, 'motd-align') === 'centre') {
      ls = ls.map(function (l) {
        var trimmed = l.slice();
        while (trimmed.length && trimmed[0].ch === ' ') trimmed.shift();
        var n = Math.max(0, Math.floor((MAX - mc.widthOf(trimmed)) / 2 / SPACE));
        var spaces = [];
        for (var i = 0; i < n; i++) spaces.push({ ch: ' ', st: mc.plain() });
        return spaces.concat(trimmed);
      });
    }
    return join(ls);
  }

  function hocon(s) { return '"' + s.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"'; }

  function update(chars) {
    var shown = shaped(chars);
    var wrapped = mc.wrap(shown, MAX);
    mc.render(motdEl, join(wrapped.slice(0, 2)), '#808080', false);
    var ls = lines(shown);
    var over = [];
    for (var i = 0; i < 2; i++) {
      var w = mc.widthOf(ls[i] || []);
      var meter = root.querySelector('[data-width="' + i + '"]');
      meter.textContent = w + ' of ' + MAX;
      meter.parentNode.classList.toggle('is-over', w > MAX);
      root.querySelector('[data-bar="' + i + '"]').style.setProperty('--w', Math.min(100, (w / MAX) * 100) + '%');
      if (w > MAX) over.push(i + 1);
    }
    warning.hidden = !over.length;
    if (over[0] === 1) warning.textContent = 'Line 1 is wider than the list: Minecraft wraps it, and ' + (ls[1] && ls[1].length ? 'line 2 drops out of sight.' : 'the rest shows as line 2.');
    else if (over[0] === 2) warning.textContent = 'Line 2 is wider than the list: its end is cut off.';
    out.props.textContent = 'motd=' + mc.properties(shown);
    out.mini.textContent = 'motds=[\n    {\n        icon=random\n        line1=' + hocon(mc.minimessage(ls[0] || [], editor.gradients)) + '\n        line2=' + hocon(mc.minimessage(ls[1] || [], editor.gradients)) + '\n    }\n]';
    out.sect.textContent = mc.legacy(shown, '§', 'x', false);
    out.amp.textContent = mc.legacy(shown, '&', 'hash', false);
    hexNote.hidden = !shown.some(function (c) { return c.st.c && !c.st.l; });
  }

  editor = window.mcEditor(root.querySelector('[data-editor]'), { lines: 2, fallback: '#c9c9c4', onChange: update });
  t.radios(root, 'motd-align').forEach(function (r) { r.addEventListener('change', function () { update(editor.chars()); }); });
  root.querySelectorAll('[data-preset]').forEach(function (b) {
    b.addEventListener('click', function () {
      editor.load(mc.parseMiniMessage(PRESETS[b.getAttribute('data-preset')], editor.gradients));
      editor.input.focus();
    });
  });

  var nameInput = root.querySelector('#motd-name');
  var showName = function () { nameEl.textContent = nameInput.value.trim() || 'A Minecraft Server'; };
  nameInput.addEventListener('input', showName);
  showName();

  // The icon: Playkeeper's pixel landscape, or yours, filled to 64 × 64.
  var ctx = icon.getContext('2d');
  var art = new Image();
  art.onload = function () { ctx.imageSmoothingEnabled = false; ctx.clearRect(0, 0, 64, 64); ctx.drawImage(art, 0, 0, 64, 64); };
  art.src = root.getAttribute('data-default-icon');
  var file = root.querySelector('#motd-icon');
  root.querySelector('[data-choose-icon]').addEventListener('click', function () { file.click(); });
  file.addEventListener('change', function () {
    var f = file.files && file.files[0];
    file.value = '';
    if (!f || !window.createImageBitmap) return;
    createImageBitmap(f).then(function (img) {
      var side = Math.min(img.width, img.height);
      ctx.clearRect(0, 0, 64, 64);
      ctx.imageSmoothingEnabled = side > 64;
      ctx.imageSmoothingQuality = 'high';
      ctx.drawImage(img, (img.width - side) / 2, (img.height - side) / 2, side, side, 0, 0, 64, 64);
    }, function () {
      t.toast('That picture couldn\'t be read. Try a PNG, JPEG or WebP.', 3000);
    });
  });

  editor.set(mc.parseMiniMessage(PRESETS.gradient, editor.gradients));
})();

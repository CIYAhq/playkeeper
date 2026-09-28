// The server icon maker (/tools/server-icon): letters on a background, or a
// picture framed by zooming and dragging, drawn onto a 64 × 64 canvas; shown
// big, at real size and in the server list; saved as server-icon.png. A
// picture is read with createImageBitmap, so it stays on the page and the
// Content-Security-Policy's img-src needs nothing more.
(function () {
  var t = window.pkTools;
  var root = document.querySelector('[data-icon-tool]');
  if (!t || !root || !window.createImageBitmap) return;
  var $ = function (sel) { return root.querySelector(sel); };
  var $$ = function (sel) { return Array.prototype.slice.call(root.querySelectorAll(sel)); };
  var SIZE = 64;
  var TOOL = 'server-icon';

  // Playkeeper's own 5 × 7 letters, drawn for the icon maker.
  var FONT = {
    A: ['.###.', '#...#', '#...#', '#####', '#...#', '#...#', '#...#'],
    B: ['####.', '#...#', '#...#', '####.', '#...#', '#...#', '####.'],
    C: ['.###.', '#...#', '#....', '#....', '#....', '#...#', '.###.'],
    D: ['####.', '#...#', '#...#', '#...#', '#...#', '#...#', '####.'],
    E: ['#####', '#....', '#....', '####.', '#....', '#....', '#####'],
    F: ['#####', '#....', '#....', '####.', '#....', '#....', '#....'],
    G: ['.###.', '#...#', '#....', '#.###', '#...#', '#...#', '.####'],
    H: ['#...#', '#...#', '#...#', '#####', '#...#', '#...#', '#...#'],
    I: ['###', '.#.', '.#.', '.#.', '.#.', '.#.', '###'],
    J: ['..###', '...#.', '...#.', '...#.', '...#.', '#..#.', '.##..'],
    K: ['#...#', '#..#.', '#.#..', '##...', '#.#..', '#..#.', '#...#'],
    L: ['#....', '#....', '#....', '#....', '#....', '#....', '#####'],
    M: ['#...#', '##.##', '#.#.#', '#.#.#', '#...#', '#...#', '#...#'],
    N: ['#...#', '#...#', '##..#', '#.#.#', '#..##', '#...#', '#...#'],
    O: ['.###.', '#...#', '#...#', '#...#', '#...#', '#...#', '.###.'],
    P: ['####.', '#...#', '#...#', '####.', '#....', '#....', '#....'],
    Q: ['.###.', '#...#', '#...#', '#...#', '#.#.#', '#..#.', '.##.#'],
    R: ['####.', '#...#', '#...#', '####.', '#.#..', '#..#.', '#...#'],
    S: ['.####', '#....', '#....', '.###.', '....#', '....#', '####.'],
    T: ['#####', '..#..', '..#..', '..#..', '..#..', '..#..', '..#..'],
    U: ['#...#', '#...#', '#...#', '#...#', '#...#', '#...#', '.###.'],
    V: ['#...#', '#...#', '#...#', '#...#', '#...#', '.#.#.', '..#..'],
    W: ['#...#', '#...#', '#...#', '#.#.#', '#.#.#', '#.#.#', '.#.#.'],
    X: ['#...#', '#...#', '.#.#.', '..#..', '.#.#.', '#...#', '#...#'],
    Y: ['#...#', '#...#', '.#.#.', '..#..', '..#..', '..#..', '..#..'],
    Z: ['#####', '....#', '...#.', '..#..', '.#...', '#....', '#####'],
    0: ['.###.', '#...#', '#..##', '#.#.#', '##..#', '#...#', '.###.'],
    1: ['.#.', '##.', '.#.', '.#.', '.#.', '.#.', '###'],
    2: ['.###.', '#...#', '....#', '...#.', '..#..', '.#...', '#####'],
    3: ['####.', '....#', '....#', '.###.', '....#', '....#', '####.'],
    4: ['...#.', '..##.', '.#.#.', '#..#.', '#####', '...#.', '...#.'],
    5: ['#####', '#....', '####.', '....#', '....#', '#...#', '.###.'],
    6: ['.###.', '#....', '#....', '####.', '#...#', '#...#', '.###.'],
    7: ['#####', '....#', '...#.', '..#..', '.#...', '.#...', '.#...'],
    8: ['.###.', '#...#', '#...#', '.###.', '#...#', '#...#', '.###.'],
    9: ['.###.', '#...#', '#...#', '.####', '....#', '....#', '.###.'],
    '!': ['#', '#', '#', '#', '#', '.', '#'],
    '?': ['.###.', '#...#', '....#', '...#.', '..#..', '.....', '..#..'],
    '&': ['.##..', '#..#.', '#.#..', '.#...', '#.#.#', '#..#.', '.##.#'],
    '+': ['.....', '..#..', '..#..', '#####', '..#..', '..#..', '.....'],
    '-': ['....', '....', '....', '####', '....', '....', '....'],
    '#': ['.#.#.', '.#.#.', '#####', '.#.#.', '#####', '.#.#.', '.#.#.'],
    '.': ['.', '.', '.', '.', '.', '.', '#'],
    ':': ['.', '#', '.', '.', '.', '#', '.'],
    '♥': ['.....', '.#.#.', '#####', '#####', '.###.', '..#..', '.....'],
    ' ': ['...', '...', '...', '...', '...', '...', '...'],
  };

  var out = document.createElement('canvas');
  out.width = out.height = SIZE;
  var octx = out.getContext('2d');
  var big = $('[data-big]');
  var views = [big, $('[data-real]'), $('[data-list-icon]'), root.querySelector('[data-dock-icon]')].filter(Boolean);
  var empty = $('[data-empty]');
  var info = $('[data-file-info]');
  var dockInfo = root.querySelector('[data-dock-info]');
  var status = $('[data-status]');
  var zoomInput = $('#icon-zoom');
  var zoomValue = $('[data-zoom-value]');
  var textInput = $('#icon-text');
  var textHint = $('[data-text-hint]');
  var fileInput = $('#icon-file');
  var drop = $('[data-drop]');
  var shuffle = $('[data-shuffle]');
  var downloads = $$('[data-download]');

  // What each colour is called, for the preview's description.
  var names = {};
  $$('.swatches input').forEach(function (r) {
    var label = root.querySelector('label[for="' + r.id + '"]');
    if (r.value && label) names[r.value] = label.getAttribute('title');
  });
  var nameOf = function (h) { return names[h] || h; };

  var state = {
    source: t.value(root, 'icon-source') || 'letters',
    text: textInput.value.toUpperCase(),
    fg: '#ffffff',
    effect: t.value(root, 'icon-effect') || 'shadow',
    bgStyle: t.value(root, 'icon-bg-style') || 'blocks',
    bg: '#3f9b3a',
    seed: 7,
    image: null,
    levels: [],
    zoom: 1,
    cx: 0,
    cy: 0,
    fit: t.value(root, 'icon-fit') || 'fill',
    smooth: t.value(root, 'icon-pixels') !== 'sharp',
    pbg: '',
  };

  // A seeded random number from 0 to 1, so the same seed draws the same blocks.
  function random(seed) {
    return function () {
      seed = (seed + 0x6d2b79f5) | 0;
      var r = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      r = (r + Math.imul(r ^ (r >>> 7), 61 | r)) ^ r;
      return ((r ^ (r >>> 14)) >>> 0) / 4294967296;
    };
  }
  function lightness(h) {
    var c = t.rgb(h);
    return (0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]) / 255;
  }

  function drawBackground(ctx) {
    var bg = state.bg;
    if (state.bgStyle === 'fade') {
      for (var i = 0; i < 8; i++) {
        ctx.fillStyle = t.shade(bg, 0.16 - (0.4 * i) / 7);
        ctx.fillRect(0, i * 8, SIZE, 8);
      }
    } else if (state.bgStyle === 'blocks') {
      var r = random(state.seed);
      var tones = [-0.18, -0.09, 0, 0.08];
      for (var y = 0; y < 16; y++) {
        for (var x = 0; x < 16; x++) {
          var p = r();
          var tone = p < 0.16 ? tones[0] : p < 0.46 ? tones[1] : p < 0.86 ? tones[2] : tones[3];
          ctx.fillStyle = tone < 0 ? t.shade(bg, tone) : tone > 0 ? t.shade(bg, tone) : bg;
          ctx.fillRect(x * 4, y * 4, 4, 4);
        }
      }
    } else {
      ctx.fillStyle = bg;
      ctx.fillRect(0, 0, SIZE, SIZE);
    }
  }

  // The letters that can be drawn, each trimmed to the columns it uses.
  function glyphs(text) {
    var list = [];
    Array.from(text).forEach(function (ch) {
      var g = FONT[ch];
      if (g) list.push({ rows: g, w: g[0].length });
    });
    return list;
  }

  function drawLetters(ctx) {
    drawBackground(ctx);
    var list = glyphs(state.text);
    if (!list.length) return;
    var units = list.reduce(function (n, g) { return n + g.w; }, 0) + list.length - 1;
    var outline = state.effect === 'outline';
    var shadow = state.effect === 'shadow';
    var room = SIZE - 8;
    var s = 1;
    // The shadow sits one of the letters' pixels down and right, as
    // Minecraft draws its text; an outline is one of them all round.
    for (var k = 12; k >= 1; k--) {
      var extra = outline ? 2 * k : shadow ? k : 0;
      if (units * k + extra <= room && 7 * k + extra <= room) { s = k; break; }
    }
    var d = shadow ? s : 0;
    var w = units * s + (outline ? 2 * s : d);
    var h = 7 * s + (outline ? 2 * s : d);
    var x0 = Math.floor((SIZE - w) / 2) + (outline ? s : 0);
    var y0 = Math.floor((SIZE - h) / 2) + (outline ? s : 0);
    var paint = function (color, dx, dy, grow) {
      ctx.fillStyle = color;
      var x = x0;
      list.forEach(function (g) {
        for (var row = 0; row < 7; row++) {
          for (var col = 0; col < g.w; col++) {
            if (g.rows[row][col] !== '#') continue;
            ctx.fillRect(x + col * s + dx - grow, y0 + row * s + dy - grow, s + 2 * grow, s + 2 * grow);
          }
        }
        x += (g.w + 1) * s;
      });
    };
    if (outline) paint(lightness(state.fg) > 0.5 ? t.shade(state.fg, -0.85) : t.shade(state.fg, 0.85), 0, 0, s);
    if (shadow) paint(t.shade(state.fg, -0.75), d, d, 0);
    paint(state.fg, 0, 0, 0);
  }

  // The picture's scale on the icon: its pixels per icon pixel, zoom included.
  function scale() {
    var img = state.image;
    var fit = state.fit === 'fill' ? Math.max(SIZE / img.width, SIZE / img.height) : Math.min(SIZE / img.width, SIZE / img.height);
    return fit * state.zoom;
  }
  // Keeps a filled square covered, and a whole picture on the icon.
  function clampCenter() {
    var img = state.image;
    if (!img) return;
    var s = scale();
    var half = SIZE / 2 / s;
    var clamp = function (v, lo, hi) { return lo > hi ? (lo + hi) / 2 : Math.min(Math.max(v, lo), hi); };
    if (state.fit === 'fill') {
      state.cx = clamp(state.cx, half, img.width - half);
      state.cy = clamp(state.cy, half, img.height - half);
    } else {
      state.cx = clamp(state.cx, 0, img.width);
      state.cy = clamp(state.cy, 0, img.height);
    }
  }
  function drawPicture(ctx) {
    if (state.pbg) {
      ctx.fillStyle = state.pbg;
      ctx.fillRect(0, 0, SIZE, SIZE);
    }
    var img = state.image;
    if (!img) return;
    var s = scale();
    var dx = SIZE / 2 - state.cx * s;
    var dy = SIZE / 2 - state.cy * s;
    var src = img;
    if (state.smooth) {
      // Halved copies, so a large picture shrinks without losing detail to
      // aliasing: the one that needs at most halving again.
      var level = s < 1 ? Math.min(state.levels.length - 1, Math.floor(Math.log2(1 / s))) : 0;
      src = state.levels[level] || img;
      ctx.imageSmoothingEnabled = true;
      ctx.imageSmoothingQuality = 'high';
    } else {
      ctx.imageSmoothingEnabled = false;
    }
    ctx.drawImage(src, dx, dy, img.width * s, img.height * s);
  }

  function describe() {
    if (state.source === 'picture') return state.image ? 'Your server icon, from your picture' : 'No picture yet';
    var list = glyphs(state.text);
    var style = { solid: 'plain', fade: 'fading', blocks: 'blocks of' }[state.bgStyle];
    var around = { none: '', shadow: ', with a shadow', outline: ', outlined' }[state.effect];
    return list.length
      ? 'Your server icon: ' + state.text.trim() + ' in ' + nameOf(state.fg) + around + ', on ' + style + ' ' + nameOf(state.bg)
      : 'Your server icon: ' + style + ' ' + nameOf(state.bg) + ', with no letters';
  }

  var sizeTimer = 0;
  function render() {
    octx.setTransform(1, 0, 0, 1, 0, 0);
    octx.clearRect(0, 0, SIZE, SIZE);
    octx.imageSmoothingEnabled = true;
    if (state.source === 'picture') drawPicture(octx);
    else drawLetters(octx);
    views.forEach(function (c) {
      var ctx = c.getContext('2d');
      ctx.imageSmoothingEnabled = false;
      ctx.clearRect(0, 0, c.width, c.height);
      ctx.drawImage(out, 0, 0, c.width, c.height);
    });
    var ready = state.source !== 'picture' || !!state.image;
    empty.hidden = ready;
    downloads.forEach(function (b) { b.disabled = !ready; });
    big.setAttribute('aria-label', describe());
    clearTimeout(sizeTimer);
    if (!ready) {
      info.textContent = 'Choose a picture first';
      if (dockInfo) dockInfo.textContent = 'Choose a picture first';
      return;
    }
    sizeTimer = setTimeout(function () {
      out.toBlob(function (blob) {
        if (!blob) return;
        var text = '64 × 64 PNG · ' + (blob.size < 1024 ? blob.size + ' bytes' : (blob.size / 1024).toFixed(1) + ' KB');
        info.textContent = text;
        if (dockInfo) dockInfo.textContent = text;
      }, 'image/png');
    }, 120);
  }

  function setSource(v) {
    state.source = v;
    t.pick(root, 'icon-source', v);
    $$('[data-panel]').forEach(function (p) { p.hidden = p.getAttribute('data-panel') !== v; });
    root.classList.toggle('is-picture', v === 'picture');
    if (v === 'picture') big.tabIndex = 0;
    else big.removeAttribute('tabindex');
    render();
  }

  function setZoom(z, keepSlider) {
    state.zoom = Math.min(8, Math.max(1, z));
    if (!keepSlider) zoomInput.value = String(Math.round(state.zoom * 100));
    zoomValue.textContent = Math.round(state.zoom * 100) + '%';
    zoomInput.style.setProperty('--fill', ((state.zoom - 1) / 7) * 100 + '%');
    clampCenter();
    render();
  }

  function levelsOf(img) {
    var levels = [img];
    var w = img.width;
    var h = img.height;
    var src = img;
    while (Math.max(w, h) > 256) {
      w = Math.max(1, Math.round(w / 2));
      h = Math.max(1, Math.round(h / 2));
      var c = document.createElement('canvas');
      c.width = w;
      c.height = h;
      var ctx = c.getContext('2d');
      ctx.imageSmoothingEnabled = true;
      ctx.imageSmoothingQuality = 'high';
      ctx.drawImage(src, 0, 0, w, h);
      levels.push(c);
      src = c;
    }
    return levels;
  }

  function load(file) {
    if (!file || !/^image\//.test(file.type || '')) {
      t.toast('That isn\'t a picture. Choose a PNG, JPEG, WebP or GIF.', 3000);
      return;
    }
    createImageBitmap(file).then(function (img) {
      if (state.image && state.image.close) state.image.close();
      state.image = img;
      drop.classList.add('has-file');
      $('[data-picture-controls]').hidden = false;
      $('[data-drop-text]').textContent = file.name && file.name !== 'image.png' ? file.name : 'Your picture';
      $('[data-choose]').textContent = 'Choose another';
      state.levels = levelsOf(img);
      state.cx = img.width / 2;
      state.cy = img.height / 2;
      // Pixel art stays sharp; photos and drawings shrink smoothly.
      var small = Math.max(img.width, img.height) <= 128;
      state.smooth = !small;
      t.pick(root, 'icon-pixels', small ? 'sharp' : 'smooth');
      setSource('picture');
      setZoom(1);
      status.textContent = 'Picture opened, ' + img.width + ' by ' + img.height + ' pixels. Drag it in the preview to move it.';
    }, function () {
      t.toast('That picture couldn\'t be read. Try a PNG, JPEG or WebP.', 3000);
    });
  }

  // Letters.
  function readText() {
    var raw = textInput.value.toUpperCase();
    var kept = Array.from(raw).filter(function (ch) { return FONT[ch]; }).join('');
    state.text = kept;
    textHint.textContent = kept.length < Array.from(raw).length
      ? 'Some of those can\'t be drawn: use letters, numbers and ! ? & + - # ♥'
      : 'Up to four: letters, numbers and ! ? & + - # ♥';
  }
  textInput.addEventListener('input', function () { readText(); render(); });
  readText();
  var fg = t.colorGroup(root, 'icon-fg', $('#icon-fg-hex'), function (v) { state.fg = v || '#ffffff'; render(); });
  var bg = t.colorGroup(root, 'icon-bg', $('#icon-bg-hex'), function (v) { state.bg = v || '#3f9b3a'; render(); });
  state.fg = fg.get() || state.fg;
  state.bg = bg.get() || state.bg;
  t.radios(root, 'icon-effect').forEach(function (r) { r.addEventListener('change', function () { state.effect = r.value; render(); }); });
  t.radios(root, 'icon-bg-style').forEach(function (r) {
    r.addEventListener('change', function () {
      state.bgStyle = r.value;
      shuffle.hidden = r.value !== 'blocks';
      render();
    });
  });
  shuffle.hidden = state.bgStyle !== 'blocks';
  shuffle.addEventListener('click', function () {
    state.seed = (state.seed + 1 + Math.floor(Math.random() * 1e6)) | 0;
    render();
  });

  // A picture: chosen, dropped anywhere on the tool, or pasted.
  $('[data-choose]').addEventListener('click', function () { fileInput.click(); });
  fileInput.addEventListener('change', function () {
    if (fileInput.files && fileInput.files[0]) load(fileInput.files[0]);
    fileInput.value = '';
  });
  var dragDepth = 0;
  var hasFiles = function (e) { return e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types || [], 'Files') !== -1; };
  root.addEventListener('dragenter', function (e) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth++;
    drop.classList.add('is-over');
  });
  root.addEventListener('dragover', function (e) { if (hasFiles(e)) e.preventDefault(); });
  root.addEventListener('dragleave', function () {
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) drop.classList.remove('is-over');
  });
  root.addEventListener('drop', function (e) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    drop.classList.remove('is-over');
    var files = Array.prototype.filter.call(e.dataTransfer.files, function (f) { return /^image\//.test(f.type); });
    if (files.length) load(files[0]);
    else t.toast('That isn\'t a picture. Choose a PNG, JPEG, WebP or GIF.', 3000);
  });
  document.addEventListener('paste', function (e) {
    var items = (e.clipboardData && e.clipboardData.items) || [];
    for (var i = 0; i < items.length; i++) {
      if (items[i].kind === 'file' && /^image\//.test(items[i].type)) {
        e.preventDefault();
        load(items[i].getAsFile());
        return;
      }
    }
  });

  zoomInput.addEventListener('input', function () { setZoom(Number(zoomInput.value) / 100, true); });
  t.radios(root, 'icon-fit').forEach(function (r) {
    r.addEventListener('change', function () {
      state.fit = r.value;
      clampCenter();
      render();
    });
  });
  t.radios(root, 'icon-pixels').forEach(function (r) { r.addEventListener('change', function () { state.smooth = r.value === 'smooth'; render(); }); });
  var pbg = t.colorGroup(root, 'icon-pbg', $('#icon-pbg-hex'), function (v) { state.pbg = v; render(); });
  state.pbg = pbg.get();

  // Moving the picture: drag it in the preview, turn the wheel over it to
  // zoom, or use the arrow keys and + and - once it has focus.
  var drag = null;
  var movable = function () { return state.source === 'picture' && !!state.image; };
  big.addEventListener('pointerdown', function (e) {
    if (!movable()) return;
    drag = { x: e.clientX, y: e.clientY, cx: state.cx, cy: state.cy };
    big.setPointerCapture(e.pointerId);
    big.classList.add('is-dragging');
    e.preventDefault();
  });
  big.addEventListener('pointermove', function (e) {
    if (!drag) return;
    var perIcon = SIZE / big.getBoundingClientRect().width / scale();
    state.cx = drag.cx - (e.clientX - drag.x) * perIcon;
    state.cy = drag.cy - (e.clientY - drag.y) * perIcon;
    clampCenter();
    render();
  });
  var endDrag = function () {
    drag = null;
    big.classList.remove('is-dragging');
  };
  big.addEventListener('pointerup', endDrag);
  big.addEventListener('pointercancel', endDrag);
  big.addEventListener('wheel', function (e) {
    if (!movable()) return;
    e.preventDefault();
    setZoom(state.zoom * (e.deltaY < 0 ? 1.1 : 1 / 1.1));
  }, { passive: false });
  big.addEventListener('keydown', function (e) {
    if (!movable()) return;
    var step = (e.shiftKey ? 8 : 1) / scale();
    var moves = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    if (moves[e.key]) {
      e.preventDefault();
      state.cx += moves[e.key][0];
      state.cy += moves[e.key][1];
      clampCenter();
      render();
    } else if (e.key === '+' || e.key === '=') {
      e.preventDefault();
      setZoom(state.zoom * 1.1);
    } else if (e.key === '-') {
      e.preventDefault();
      setZoom(state.zoom / 1.1);
    }
  });

  t.radios(root, 'icon-source').forEach(function (r) { r.addEventListener('change', function () { setSource(r.value); }); });

  var nameInput = $('#icon-name');
  var listName = $('[data-list-name]');
  var showName = function () { listName.textContent = nameInput.value.trim() || 'A Minecraft Server'; };
  nameInput.addEventListener('input', showName);
  showName();

  downloads.forEach(function (b) {
    b.addEventListener('click', function () {
      if (b.disabled) return;
      out.toBlob(function (blob) {
        if (!blob) {
          t.toast('The icon couldn\'t be saved. Try again.', 3000);
          return;
        }
        t.download(blob, 'server-icon.png', TOOL);
        status.textContent = 'server-icon.png downloaded.';
      }, 'image/png');
    });
  });

  // On a phone the icon and Download stay at hand in a bar at the bottom
  // while the controls scroll and the preview is out of view.
  var dock = root.querySelector('[data-dock]');
  var preview = $('[data-preview]');
  var controls = $('.tool-controls');
  if (dock && 'IntersectionObserver' in window) {
    var seen = { preview: true, controls: false };
    var watch = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) { seen[en.target === preview ? 'preview' : 'controls'] = en.isIntersecting; });
      var shown = !seen.preview && seen.controls;
      dock.classList.toggle('is-shown', shown);
      dock.toggleAttribute('inert', !shown);
    });
    watch.observe(preview);
    watch.observe(controls);
  }

  setSource(state.source);
  zoomValue.textContent = zoomInput.value + '%';
  zoomInput.style.setProperty('--fill', ((Number(zoomInput.value) / 100 - 1) / 7) * 100 + '%');
})();

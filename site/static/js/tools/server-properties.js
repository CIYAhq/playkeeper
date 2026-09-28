// The server.properties editor (/tools/server-properties): a control for
// each setting the page lists (internal/site/properties.go draws the rows,
// with each key's kind and default), the file with every setting in the
// order the server writes them, and a file of your own read in the way
// java.util.Properties reads it.
(function () {
  var t = window.pkTools;
  var root = document.querySelector('[data-sp-tool]');
  if (!t || !root) return;
  var TOOL = 'server-properties';
  var HEADER = ['#Minecraft server properties', '#Made at playkeeper.io/tools/server-properties'];
  var LEGACY = { difficulty: ['peaceful', 'easy', 'normal', 'hard'], gamemode: ['survival', 'creative', 'adventure', 'spectator'] };
  var $ = function (s) { return root.querySelector(s); };
  var out = $('[data-out]');
  var count = $('[data-count]');
  var dockInfo = $('[data-dock-info]');
  var search = $('#sp-search');
  var changedOnly = $('#sp-changed');
  var empty = $('[data-empty]');
  var other = $('[data-other]');
  var otherRows = $('[data-other-rows]');
  var retired = {};
  root.querySelectorAll('[data-retired] li').forEach(function (li) { retired[li.getAttribute('data-key')] = li.textContent; });

  // ---------- The settings ----------

  var props = [];
  var byKey = {};
  root.querySelectorAll('[data-prop]').forEach(function (row) {
    var p = {
      key: row.getAttribute('data-prop'),
      kind: row.getAttribute('data-kind'),
      def: row.getAttribute('data-default'),
      min: row.hasAttribute('data-min') ? Number(row.getAttribute('data-min')) : null,
      max: row.hasAttribute('data-max') ? Number(row.getAttribute('data-max')) : null,
      row: row,
      input: row.querySelector('.sp-control input'),
      radios: Array.prototype.slice.call(row.querySelectorAll('.sp-control input[type="radio"]')),
      note: row.querySelector('[data-note]'),
      reset: row.querySelector('[data-reset]'),
      defaultText: row.querySelector('[data-default-text]'),
      words: (row.getAttribute('data-prop') + ' ' + row.querySelector('.sp-what').textContent).toLowerCase(),
      known: true,
    };
    p.defaultHTML = p.defaultText.innerHTML;
    props.push(p);
    byKey[p.key] = p;
  });

  function get(p) {
    if (!p.known) return p.input.value;
    if (p.kind === 'bool') return p.input.checked ? 'true' : 'false';
    if (p.kind === 'choice') {
      var on = p.radios.filter(function (r) { return r.checked; })[0];
      return on ? on.value : p.def;
    }
    // The server reads a number with Integer.parseInt, which a space fails.
    if (p.kind === 'int') return p.input.value.trim();
    return p.input.value;
  }

  // set puts a value from a file into a setting's control. A choice takes
  // the old numbers and a level type without its namespace, as the server
  // does; one it doesn't know stays at the default, with a note.
  function set(p, v) {
    p.loaded = null;
    if (!p.known || p.kind === 'int' || p.kind === 'text') {
      p.input.value = v;
    } else if (p.kind === 'bool') {
      p.input.checked = v.trim().toLowerCase() === 'true';
    } else {
      var want = v.trim().toLowerCase();
      if (LEGACY[p.key] && /^[0-3]$/.test(want)) want = LEGACY[p.key][Number(want)];
      if (p.key === 'level-type' && want.indexOf(':') < 0) want = 'minecraft:' + want;
      var match = p.radios.filter(function (r) { return r.value === want; })[0];
      if (!match) {
        p.loaded = v;
        match = p.radios.filter(function (r) { return r.value === p.def; })[0];
      }
      match.checked = true;
    }
  }

  // ---------- Writing the file ----------

  function hex4(n) { return ('000' + n.toString(16).toUpperCase()).slice(-4); }

  // escape writes a value as java.util.Properties does: \, :, =, # and !
  // escaped, a leading space too, and anything past ASCII as \uXXXX. A MOTD
  // typed with \u00A7 or \n in it keeps them as codes.
  function escape(v, key) {
    if (key === 'motd') v = v.replace(/\\u([0-9a-fA-F]{4})/g, function (m, h) { return String.fromCharCode(parseInt(h, 16)); }).replace(/\\n/g, '\n');
    var s = '';
    for (var i = 0; i < v.length; i++) {
      var c = v[i];
      var n = v.charCodeAt(i);
      if (c === '\\' || c === ':' || c === '=' || c === '#' || c === '!' || (c === ' ' && i === 0)) s += '\\' + c;
      else if (c === '\t') s += '\\t';
      else if (c === '\n') s += '\\n';
      else if (c === '\r') s += '\\r';
      else if (c === '\f') s += '\\f';
      else if (n < 0x20 || n > 0x7e) s += '\\u' + hex4(n);
      else s += c;
    }
    return s;
  }

  function escapeHTML(s) { return s.replace(/[&<>"']/g, function (c) { return '&#' + c.charCodeAt(0) + ';'; }); }

  function changed(p) { return !p.known || get(p) !== p.def; }

  function lines() {
    return props.slice().sort(function (a, b) { return a.key < b.key ? -1 : a.key > b.key ? 1 : 0; }).map(function (p) {
      return { text: p.key + '=' + escape(get(p), p.key), changed: changed(p) };
    });
  }

  function file() { return HEADER.concat(lines().map(function (l) { return l.text; })).join('\n'); }

  // ---------- What's wrong with a value ----------

  function value(key) { return byKey[key] ? get(byKey[key]) : ''; }
  function on(key) { return value(key) === 'true'; }

  function problem(p) {
    var v = get(p);
    if (!p.known) return retired[p.key] || 'Not a setting 26.3 reads. The server keeps it as it is, for a plugin that might.';
    if (p.loaded !== null && p.loaded !== undefined) return 'Your file had ' + p.loaded + ', which 26.3 doesn’t know, so it uses ' + p.def + '.';
    if (p.kind === 'int') {
      if (!/^-?\d+$/.test(v)) return 'Not a whole number, so the server would use ' + p.def + '.';
      var n = Number(v);
      if ((p.min !== null && n < p.min) || (p.max !== null && n > p.max)) return p.max !== null ? 'It can be ' + p.min + ' to ' + p.max + '.' : 'It can be ' + p.min + ' or more.';
    }
    switch (p.key) {
      case 'online-mode': return v === 'false' ? 'Anyone can join under any name, unless a proxy in front checks accounts.' : '';
      case 'rcon.password': return on('enable-rcon') && !v ? 'RCON won’t start without a password.' : '';
      case 'require-resource-pack': return v === 'true' && !value('resource-pack') ? 'There’s no resource-pack to require.' : '';
      case 'resource-pack-sha1': return v && !/^[0-9a-f]{40}$/.test(v) ? 'A SHA-1 is 40 characters from 0–9 and a–f.' : '';
      case 'resource-pack-id': return v && !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v) ? 'Not a UUID, like 123e4567-e89b-12d3-a456-426614174000.' : '';
      case 'management-server-secret': return v && !/^[A-Za-z0-9]{40}$/.test(v) ? 'It must be 40 letters and digits, or empty for the server to make one.' : '';
      case 'management-server-tls-keystore': return on('management-server-enabled') && on('management-server-tls-enabled') && !v ? 'The server won’t start like this: the API has TLS on and no keystore.' : '';
    }
    return '';
  }

  // ---------- Showing it ----------

  function filter() {
    var words = search.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    var shown = 0;
    props.forEach(function (p) {
      var hit = words.every(function (w) { return p.words.indexOf(w) >= 0; }) && (!changedOnly.checked || changed(p));
      p.row.hidden = !hit;
      if (hit) shown++;
    });
    root.querySelectorAll('[data-group], [data-other]').forEach(function (g) {
      var rows = g.querySelectorAll('[data-prop]');
      g.hidden = !rows.length || Array.prototype.every.call(rows, function (r) { return r.hidden; });
    });
    empty.hidden = shown > 0;
    if (!shown) empty.textContent = words.length ? 'No setting matches “' + search.value.trim() + '”.' : 'Nothing’s changed yet.';
  }

  function update() {
    var n = 0;
    props.forEach(function (p) {
      var isChanged = changed(p);
      if (isChanged) n++;
      p.row.classList.toggle('is-changed', isChanged);
      if (p.known) {
        p.reset.hidden = !isChanged;
        p.defaultText.innerHTML = isChanged ? 'Changed from ' + (p.def ? '<code>' + escapeHTML(p.def) + '</code>' : 'empty') : p.defaultHTML;
      }
      var why = problem(p);
      p.note.hidden = !why;
      p.note.textContent = why;
      if (p.kind === 'int' && p.known) p.input.toggleAttribute('aria-invalid', !/^-?\d+$/.test(get(p)));
    });
    var text = n === 0 ? 'No changes' : n === 1 ? '1 changed' : n + ' changed';
    count.textContent = text;
    dockInfo.textContent = text;
    out.innerHTML = HEADER.map(escapeHTML).concat(lines().map(function (l) {
      return l.changed ? '<span class="sp-changed-line">' + escapeHTML(l.text) + '</span>' : escapeHTML(l.text);
    })).join('\n');
    filter();
  }

  props.forEach(function (p) {
    (p.radios.length ? p.radios : [p.input]).forEach(function (el) {
      el.addEventListener(el.type === 'checkbox' || el.type === 'radio' ? 'change' : 'input', function () {
        p.loaded = null;
        update();
      });
    });
    p.reset.addEventListener('click', function () {
      set(p, p.def);
      update();
      (p.radios.filter(function (r) { return r.checked; })[0] || p.input).focus();
    });
  });
  search.addEventListener('input', filter);
  search.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && search.value) {
      search.value = '';
      filter();
    }
  });
  changedOnly.addEventListener('change', filter);

  // ---------- Reading a file ----------

  function unescapeProperty(s) {
    return s.replace(/\\(?:u([0-9a-fA-F]{4})|([\s\S]))/g, function (m, h, c) {
      if (h) return String.fromCharCode(parseInt(h, 16));
      return { t: '\t', n: '\n', r: '\r', f: '\f' }[c] || c;
    });
  }

  // parse reads text as java.util.Properties.load does: comments, lines that
  // go on after a backslash, and a key ended by =, : or a space.
  function parse(text) {
    var found = [];
    var raw = text.split(/\r\n|\r|\n/);
    for (var i = 0; i < raw.length; i++) {
      var line = raw[i].replace(/^[ \t\f]+/, '');
      if (!line || line[0] === '#' || line[0] === '!') continue;
      while (/(?:^|[^\\])(?:\\\\)*\\$/.test(line) && i + 1 < raw.length) line = line.slice(0, -1) + raw[++i].replace(/^[ \t\f]+/, '');
      var k = 0;
      while (k < line.length && !/[=: \t\f]/.test(line[k])) k += line[k] === '\\' ? 2 : 1;
      var key = line.slice(0, k);
      while (k < line.length && /[ \t\f]/.test(line[k])) k++;
      if (line[k] === '=' || line[k] === ':') k++;
      while (k < line.length && /[ \t\f]/.test(line[k])) k++;
      found.push([unescapeProperty(key), unescapeProperty(line.slice(k))]);
    }
    return found;
  }

  function addOther(key, v) {
    var row = document.createElement('div');
    row.className = 'sp-row sp-row-text';
    row.setAttribute('data-prop', key);
    var id = 'sp-other-' + props.length;
    row.innerHTML = '<div class="sp-info"><p class="sp-key"><label for="' + id + '"><code></code></label></p><p class="sp-what"></p><p class="sp-default"><button type="button" class="sp-reset" data-remove>Remove it</button></p><p class="sp-note" data-note hidden></p></div>' +
      '<div class="sp-control"><input class="tool-input tool-input-mono sp-input" id="' + id + '" autocomplete="off" spellcheck="false"></div>';
    row.querySelector('code').textContent = key;
    row.querySelector('.sp-what').textContent = retired[key] ? 'A setting from an older version.' : 'From your file.';
    var p = { key: key, kind: 'text', def: '', row: row, input: row.querySelector('input'), radios: [], note: row.querySelector('[data-note]'), words: key.toLowerCase(), known: false };
    p.input.value = v;
    p.input.addEventListener('input', update);
    row.querySelector('[data-remove]').addEventListener('click', function () {
      props.splice(props.indexOf(p), 1);
      delete byKey[key];
      row.remove();
      update();
    });
    otherRows.appendChild(row);
    props.push(p);
    byKey[key] = p;
  }

  function load(text) {
    var found = parse(text);
    if (!found.length) {
      t.toast('No settings in that. Paste the lines of a server.properties file.', 3000);
      return false;
    }
    props.filter(function (p) { return !p.known; }).forEach(function (p) { p.row.remove(); });
    props = props.filter(function (p) { return p.known; });
    byKey = {};
    props.forEach(function (p) { byKey[p.key] = p; set(p, p.def); });
    found.forEach(function (kv) {
      if (byKey[kv[0]]) set(byKey[kv[0]], kv[1]);
      else addOther(kv[0], kv[1]);
    });
    update();
    t.toast('Read ' + found.length + (found.length === 1 ? ' setting' : ' settings') + ' from your file.', 2400);
    return true;
  }

  var panel = $('[data-load-panel]');
  var loadButton = $('[data-load]');
  var paste = $('#sp-paste');
  var fileInput = $('#sp-file');
  loadButton.addEventListener('click', function () {
    panel.hidden = !panel.hidden;
    loadButton.setAttribute('aria-expanded', String(!panel.hidden));
    if (!panel.hidden) paste.focus();
  });
  $('[data-use]').addEventListener('click', function () {
    if (load(paste.value)) {
      panel.hidden = true;
      loadButton.setAttribute('aria-expanded', 'false');
    }
  });
  $('[data-choose]').addEventListener('click', function () { fileInput.click(); });
  fileInput.addEventListener('change', function () {
    var f = fileInput.files && fileInput.files[0];
    if (!f) return;
    f.text().then(function (text) {
      paste.value = text;
      if (load(text)) {
        panel.hidden = true;
        loadButton.setAttribute('aria-expanded', 'false');
      }
    });
    fileInput.value = '';
  });

  root.querySelectorAll('[data-download]').forEach(function (b) {
    b.addEventListener('click', function () { t.download(new Blob([file() + '\n'], { type: 'text/plain' }), 'server.properties', TOOL); });
  });

  // Below the side-by-side width, Download stays at hand in a bar at the
  // bottom while the settings scroll and the file is out of view.
  var dock = $('[data-dock]');
  var preview = $('[data-preview]');
  var controls = $('[data-controls]');
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

  update();
})();

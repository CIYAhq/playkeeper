// The share page: reads a server template from this page's address after #,
// shows what it holds, and sends the visitor to their own dashboard with it.
// Browsers never send what follows #, and this page makes no requests, so the
// template stays in the browser. The format is in internal/templates.
(function () {
  var MAX_LINK = 16384; // templates.MaxLinkLength
  var MAX_JSON = 131072; // templates.MaxFileSize
  var LINK_FORMAT = 1; // the first byte of a link's data
  var FORMAT = 1; // templates.Format
  var STORE = 'playkeeper.dashboard';
  var HANDOFF = ['ready', 'newer', 'nopreview'];
  var TYPES = { paper: 'Paper', purpur: 'Purpur', vanilla: 'Vanilla', fabric: 'Fabric', quilt: 'Quilt', neoforge: 'NeoForge' };
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  var form = document.getElementById('open');
  var input = document.getElementById('dashboard');
  var status = document.getElementById('open-status');
  var payload = '';
  var run = 0;

  function show(state) {
    document.body.setAttribute('data-state', state);
    var parts = document.querySelectorAll('[data-show]');
    for (var i = 0; i < parts.length; i++) {
      parts[i].hidden = parts[i].getAttribute('data-show').split(' ').indexOf(state) < 0;
    }
  }

  function put(id, value) {
    document.getElementById(id).textContent = value;
  }

  // text returns a template's text for showing, only ever as text. Text that
  // internal/templates refuses, with control or invisible formatting
  // characters or longer than the format allows, makes the template damaged.
  function text(s, max) {
    if (s === undefined) return '';
    if (typeof s !== 'string' || new RegExp('[\\p{Cc}\\p{Cf}\\p{Zl}\\p{Zp}]', 'u').test(s) || Array.from(s).length > max) {
      throw new Error('not template text');
    }
    return s;
  }

  // day returns the day a template was made as "25 Sep 2026", or '' when it
  // doesn't say. A day internal/templates refuses makes the template
  // damaged. Who made it is never shown: a template can name anyone.
  function day(s) {
    var v = text(s, 10);
    if (!v) return '';
    var d = new Date(v + 'T00:00:00Z');
    if (!/^\d{4}-\d{2}-\d{2}$/.test(v) || isNaN(d.getTime()) || d.toISOString().slice(0, 10) !== v || d.getUTCFullYear() < 2020) {
      throw new Error('not a day');
    }
    return d.getUTCDate() + ' ' + MONTHS[d.getUTCMonth()] + ' ' + d.getUTCFullYear();
  }

  function list(names) {
    var shown = names.slice(0, 5);
    if (names.length > shown.length) shown.push(names.length - shown.length + ' more');
    return shown.length < 2 ? shown.join('') : shown.slice(0, -1).join(', ') + ' and ' + shown[shown.length - 1];
  }

  function base64url(s) {
    // A link cut at any point can end inside a base64 group.
    if (s.length % 4 === 1) s = s.slice(0, -1);
    var bin = atob(s.replace(/-/g, '+').replace(/_/g, '/'));
    var out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  // canOpen reports whether this browser can check and decompress a
  // template. Without that, the page still sends the template on.
  function canOpen() {
    try {
      new DecompressionStream('deflate-raw');
    } catch (e) {
      return false;
    }
    return !!(window.crypto && crypto.subtle && window.TextDecoder && Blob.prototype.stream && Array.from);
  }

  // inflate decompresses the template, reading no more than a template can be.
  function inflate(body) {
    var reader = new Blob([body]).stream().pipeThrough(new DecompressionStream('deflate-raw')).getReader();
    var chunks = [];
    var size = 0;
    function next() {
      return reader.read().then(function (r) {
        if (r.done) {
          var all = new Uint8Array(size);
          var at = 0;
          chunks.forEach(function (c) { all.set(c, at); at += c.length; });
          return new TextDecoder('utf-8', { fatal: true }).decode(all);
        }
        size += r.value.length;
        if (size > MAX_JSON) {
          reader.cancel();
          throw new Error('larger than a template can be');
        }
        chunks.push(r.value);
        return next();
      });
    }
    return next();
  }

  function summarize(t) {
    if (!t || typeof t.playkeeperTemplate !== 'number') return 'damaged';
    if (t.playkeeperTemplate > FORMAT) return 'newer';
    if (t.playkeeperTemplate !== FORMAT || !t.server) return 'damaged';
    var name = text(t.name, 32);
    var type = text(t.server.type, 32);
    var version = text(t.server.minecraftVersion, 64);
    if (!name || !type || !version) return 'damaged';
    var addons = (t.addons || []).map(function (a) { return text(a.name, 64); });
    var packs = (t.packs || []).map(function (p) {
      return text(p.name, 64) + (p.kind === 'resource' ? ' (resource pack)' : ' (data pack)');
    });
    var modpack = t.modpack ? text(t.modpack.name, 64) : '';
    var made = day(t.created);
    put('t-name', name);
    put('t-description', text(t.description, 280));
    put('t-made', made ? 'Made ' + made : '');
    document.getElementById('t-made').hidden = !made;
    put('t-server', (TYPES.hasOwnProperty(type) ? TYPES[type] : type) + ', Minecraft ' + version);
    put('t-modpack', modpack);
    put('t-addons', addons.length ? list(addons) : 'None');
    put('t-packs', list(packs));
    document.getElementById('row-modpack').hidden = !modpack;
    document.getElementById('row-packs').hidden = !packs.length;
    document.title = name + ' — a server template for Playkeeper';
    return 'ready';
  }

  // read checks the link as internal/templates does, and returns the
  // page's state, or a promise of it.
  function read(s) {
    if (s === null) return 'damaged';
    if (s === '') return 'empty';
    // The first character is the top of the format byte: A to D for
    // formats 0 to 15.
    if (!/^[A-D][A-Za-z0-9_-]*$/.test(s) || s.length > MAX_LINK) return 'damaged';
    var raw;
    try {
      raw = base64url(s);
    } catch (e) {
      return 'damaged';
    }
    if (raw.length === 0) return 'incomplete';
    if (raw[0] > LINK_FORMAT) return 'newer';
    if (raw[0] !== LINK_FORMAT) return 'damaged';
    if (raw.length < 7) return 'incomplete';
    var length = raw[1] << 8 | raw[2];
    var body = raw.subarray(7);
    if (body.length < length) return 'incomplete';
    if (body.length > length) return 'damaged';
    if (!canOpen()) return 'nopreview';
    return crypto.subtle.digest('SHA-256', body).then(function (sum) {
      var check = new Uint8Array(sum, 0, 4);
      for (var i = 0; i < 4; i++) {
        if (check[i] !== raw[3 + i]) return 'damaged';
      }
      return inflate(body).then(function (json) { return summarize(JSON.parse(json)); });
    }).then(null, function () { return 'damaged'; });
  }

  // fragment is the link's data after #, without what chat apps add
  // around it; null when it cannot be read.
  function fragment() {
    var s = window.location.hash.slice(1).replace(/^template=/, '');
    try {
      s = decodeURIComponent(s);
    } catch (e) {
      return null;
    }
    return s.replace(/\s+/g, '').replace(/[^A-Za-z0-9_-]+$/, '');
  }

  function start() {
    var mine = ++run;
    var s = fragment();
    var finish = function (state) {
      if (mine !== run) return;
      payload = HANDOFF.indexOf(state) >= 0 ? s : '';
      show(state);
    };
    payload = '';
    show('loading');
    var state = read(s);
    if (typeof state === 'string') finish(state);
    else state.then(finish);
  }

  // dashboard turns what the visitor typed into their dashboard's origin, or
  // '' when it is not an HTTPS address. An address typed without https://,
  // such as 203.0.113.7, gets Playkeeper's default port unless it has one.
  function dashboard(value) {
    var typed = value.replace(/\s+/g, '');
    var bare = !/^[a-z][a-z0-9+.-]*:\/\//i.test(typed);
    var u;
    try {
      u = new URL(bare ? 'https://' + typed : typed);
    } catch (e) {
      return '';
    }
    if (u.protocol !== 'https:' || !u.hostname || u.username || u.password) return '';
    if (bare && !u.port && !/^[^\/?#]*:443(?:[\/?#]|$)/.test(typed)) u.port = '8443';
    return u.origin;
  }

  function remembered() {
    try {
      return window.localStorage.getItem(STORE) || '';
    } catch (e) {
      return '';
    }
  }

  function remember(origin) {
    try {
      window.localStorage.setItem(STORE, origin);
    } catch (e) {
      // Storage is off in this browser: the address is asked for again.
    }
  }

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    if (!payload) return;
    var origin = dashboard(input.value);
    if (!origin) {
      status.textContent = "Enter your dashboard's https:// address, such as https://203.0.113.7:8443.";
      input.focus();
      return;
    }
    remember(origin);
    status.textContent = 'Opening ' + origin + '…';
    window.location.assign(origin + '/servers/new#template=' + payload);
  });
  input.value = remembered();
  window.addEventListener('hashchange', start);
  start();
})();

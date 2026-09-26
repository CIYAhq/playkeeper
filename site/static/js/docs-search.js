// The docs search: loads the index of the docs' parts the first time the box
// is used, and lists the parts whose title or text has every word typed.
// Ctrl+K (⌘K on a Mac) jumps to the box.
(function () {
  var box = document.querySelector('[data-search]');
  if (!box) return;
  var input = box.querySelector('input');
  var list = box.querySelector('ul');
  var kbd = box.querySelector('[data-kbd]');
  var mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  if (kbd && !mac) kbd.textContent = 'Ctrl K';
  var index = null;
  var loading = false;
  var active = -1;

  function load() {
    if (index || loading) return;
    loading = true;
    var s = document.createElement('script');
    s.src = box.getAttribute('data-index');
    s.onload = function () { index = window.playkeeperDocs || []; search(); };
    document.head.appendChild(s);
  }

  function norm(s) { return s.toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, ''); }

  function results(q) {
    var words = norm(q).split(/\s+/).filter(Boolean);
    if (!words.length) return [];
    var out = [];
    index.forEach(function (e) {
      var title = norm(e.t), text = norm(e.x + ' ' + e.p);
      var score = 0;
      for (var i = 0; i < words.length; i++) {
        var w = words[i];
        if (title.indexOf(w) >= 0) score += 3;
        else if (text.indexOf(w) >= 0) score += 1;
        else return;
      }
      out.push({ e: e, score: score });
    });
    out.sort(function (a, b) { return b.score - a.score; });
    return out.slice(0, 8).map(function (r) { return r.e; });
  }

  function select(i) {
    var items = list.querySelectorAll('[role="option"]');
    if (!items.length) return;
    active = (i + items.length) % items.length;
    for (var n = 0; n < items.length; n++) items[n].setAttribute('aria-selected', n === active ? 'true' : 'false');
    input.setAttribute('aria-activedescendant', items[active].id);
    items[active].scrollIntoView({ block: 'nearest' });
  }

  function search() {
    var q = input.value.trim();
    list.textContent = '';
    active = -1;
    input.removeAttribute('aria-activedescendant');
    if (!q || !index) {
      list.hidden = true;
      input.setAttribute('aria-expanded', 'false');
      return;
    }
    var found = results(q);
    if (!found.length) {
      var none = document.createElement('li');
      none.className = 'empty';
      none.textContent = 'Nothing in the docs matches "' + q + '".';
      list.appendChild(none);
    }
    found.forEach(function (e, i) {
      var li = document.createElement('li');
      li.id = 'docs-result-' + i;
      li.setAttribute('role', 'option');
      li.setAttribute('aria-selected', 'false');
      var a = document.createElement('a');
      a.href = e.u;
      a.tabIndex = -1;
      var strong = document.createElement('strong');
      strong.textContent = e.t;
      var span = document.createElement('span');
      span.textContent = (e.t === e.p ? '' : e.p + ' · ') + e.x;
      a.appendChild(strong);
      a.appendChild(span);
      li.appendChild(a);
      list.appendChild(li);
    });
    list.hidden = false;
    input.setAttribute('aria-expanded', 'true');
  }

  input.addEventListener('focus', load);
  input.addEventListener('input', function () { load(); search(); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); select(active + 1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); select(active - 1); }
    else if (e.key === 'Enter') {
      var items = list.querySelectorAll('[role="option"] a');
      var go = items[active >= 0 ? active : 0];
      if (go) { e.preventDefault(); window.location.href = go.href; }
    } else if (e.key === 'Escape') {
      input.value = '';
      search();
    }
  });
  document.addEventListener('keydown', function (e) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      input.focus();
      input.select();
    }
  });
  document.addEventListener('click', function (e) {
    if (!box.contains(e.target)) { list.hidden = true; input.setAttribute('aria-expanded', 'false'); }
  });
})();

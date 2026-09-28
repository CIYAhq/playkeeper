// The template directory: search, filters and sorting over every template
// at once, from js/templates-index.js, loaded the first time it's needed.
// The address keeps what's chosen (/templates?q=…&loader=paper&sort=new), so
// a link or a reload shows the same results, and the back button steps
// through pages. Without this script the pages still list and link every
// template; with it, results change at once, without fading.
(function () {
  var root = document.querySelector('[data-directory]');
  if (!root) return;
  // Open in my dashboard (site.js) on a card this script drew, which links
  // /t/<id>: that page's refresh has the template.
  if (window.playkeeperSite && window.fetch) {
    window.playkeeperSite.dashboard.find = function (link) {
      return fetch(link.getAttribute('href'), { credentials: 'omit' })
        .then(function (r) { return r.ok ? r.text() : ''; })
        .then(function (html) {
          var m = /url=\/t#([A-Za-z0-9_-]+)/.exec(html);
          return m ? m[1] : '';
        });
    };
  }
  var $ = function (sel, el) { return (el || document).querySelector(sel); };
  var $$ = function (sel, el) { return Array.prototype.slice.call((el || document).querySelectorAll(sel)); };
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
  var wide = window.matchMedia('(min-width: 1024px)');

  var PER_PAGE = 24;
  // Filters with several values at once; memory is one at a time.
  var KEYS = ['mode', 'type', 'loader', 'version', 'tag'];
  var SORTS = { relevance: 'Best match', popular: 'Popular', new: 'Newest', name: 'A–Z' };
  var ICON = {
    left: '<svg class="icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m12 19-7-7 7-7M19 12H5"/></svg>',
    right: '<svg class="icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12h14M12 5l7 7-7 7"/></svg>',
    x: '<svg class="icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>'
  };

  var base = root.getAttribute('data-base');
  var locked = root.getAttribute('data-category') || '';
  var serverPage = parseInt(root.getAttribute('data-page'), 10) || 1;
  var grid = $('[data-dir-grid]', root);
  var countEl = $('[data-dir-count]', root);
  var chips = $('[data-dir-chips]', root);
  var empty = $('[data-dir-empty]', root);
  var pagesNav = $('[data-dir-pages]', root);
  var side = $('[data-filters]', root);
  var scrim = $('[data-filters-scrim]', root);
  var filterBtn = $('[data-open-filters]', root);
  var filterN = $('[data-filter-n]', root);
  var showBtn = $('[data-show-results]', root);
  var sortBtn = $('[data-sort-btn]', root);
  var sortMenu = $('[data-sort-menu]', root);
  var sortLabel = $('[data-sort-label]', root);
  var shell = document.getElementById('dir-card-tpl');
  var input = $('[data-dir-q]');

  // What each filter's values are called, from the page's own filters.
  var labels = {};
  $$('.facet', root).forEach(function (f) {
    var key = f.getAttribute('data-facet');
    labels[key] = {};
    $$('.facet-opt', f).forEach(function (opt) { labels[key][$('input', opt).value] = $('.facet-label', opt).textContent; });
  });

  var index = null;
  var loading = null;
  var state = read();

  function blank() { return { q: '', mode: [], type: [], loader: [], version: [], tag: [], memory: '', sort: '', page: 1 }; }

  function read() {
    var s = blank();
    var p = new URLSearchParams(location.search);
    s.q = (p.get('q') || '').trim().slice(0, 80);
    KEYS.forEach(function (k) { s[k] = (p.get(k) || '').split(',').filter(Boolean); });
    if (locked) s.mode = s.mode.filter(function (m) { return m !== locked; });
    s.memory = /^\d{1,3}$/.test(p.get('memory') || '') ? p.get('memory') : '';
    s.sort = SORTS.hasOwnProperty(p.get('sort')) && (s.q || p.get('sort') !== 'relevance') ? p.get('sort') : '';
    s.page = parseInt(p.get('page'), 10) || (filtering(s) ? 1 : serverPage);
    return s;
  }

  function filtering(s) {
    return !!(s.q || s.memory || KEYS.some(function (k) { return s[k].length; }));
  }

  function sortOf(s) { return s.sort || (s.q ? 'relevance' : 'popular'); }

  // changed: what's chosen isn't what the page was built with, the most
  // popular templates on its page.
  function changed(s) { return filtering(s) || sortOf(s) !== 'popular' || s.page !== serverPage; }

  function address(s, page) {
    var parts = [];
    var put = function (k, v) { parts.push(k + '=' + encodeURIComponent(v).replace(/%2C/g, ',')); };
    if (s.q) put('q', s.q);
    KEYS.forEach(function (k) { if (s[k].length) put(k, s[k].join(',')); });
    if (s.memory) put('memory', s.memory);
    if (s.sort && s.sort !== (s.q ? 'relevance' : 'popular')) put('sort', s.sort);
    if (!parts.length) return page > 1 ? base + '/page/' + page : base;
    if (page > 1) put('page', page);
    return base + '?' + parts.join('&');
  }

  function load() {
    if (index) return Promise.resolve(index);
    if (loading) return loading;
    root.classList.add('is-loading');
    loading = new Promise(function (resolve, reject) {
      var s = document.createElement('script');
      s.src = root.getAttribute('data-index');
      s.async = true;
      s.onload = function () {
        index = prepare(window.playkeeperTemplates);
        root.classList.remove('is-loading');
        resolve(index);
      };
      s.onerror = function () { loading = null; root.classList.remove('is-loading'); reject(new Error('the template index did not load')); };
      document.head.appendChild(s);
    });
    return loading;
  }

  function norm(s) {
    return String(s).toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '').replace(/[^a-z0-9.]+/g, ' ').trim();
  }

  function prepare(data) {
    var list = data.templates.filter(function (t) { return !locked || t.cats.indexOf(locked) >= 0; });
    list.forEach(function (t, i) {
      t.rank = i;
      t.nameWords = ' ' + norm(t.name);
      t.words = ' ' + norm([t.name, t.desc, t.addons.join(' '),
        t.cats.map(function (c) { return data.cats[c]; }).join(' '),
        t.tags.map(function (g) { return data.tags[g] + ' ' + (data.tagSearch[g] || ''); }).join(' '),
        data.loaders[t.loader].name, t.version, data.kinds[t.kind]].join(' '));
    });
    return { data: data, list: list };
  }

  // score is how well a template matches every word searched for: a word
  // that starts a word of its name counts most; 0 when a word isn't there.
  function score(t, terms) {
    var total = 0;
    for (var i = 0; i < terms.length; i++) {
      var w = terms[i];
      var s = t.nameWords.indexOf(' ' + w) >= 0 ? 10 : t.nameWords.indexOf(w) > 0 ? 6 : t.words.indexOf(' ' + w) >= 0 ? 3 : t.words.indexOf(w) > 0 ? 1 : 0;
      if (!s) return 0;
      total += s;
    }
    return total;
  }

  // matches reports whether a template has what's chosen, leaving out one
  // filter (skip) to count that filter's options. Game modes, types,
  // loaders and versions are any of those chosen; features are all of them.
  function matches(t, s, skip) {
    var any = function (key, values) { return skip === key || !s[key].length || s[key].some(function (v) { return values.indexOf(v) >= 0; }); };
    return any('mode', t.cats) && any('type', [t.kind]) && any('loader', [t.loader]) && any('version', [t.version]) &&
      (skip === 'tag' || s.tag.every(function (g) { return t.tags.indexOf(g) >= 0; })) &&
      (skip === 'memory' || !s.memory || t.mb <= s.memory * 1024);
  }

  function results(s) {
    var terms = norm(s.q).split(' ').filter(Boolean);
    var by = sortOf(s);
    var found = index.list.filter(function (t) {
      t.score = terms.length ? score(t, terms) : 1;
      return t.score > 0 && matches(t, s);
    });
    found.sort(function (a, b) {
      if (by === 'relevance') return (b.score - a.score) || (a.rank - b.rank);
      if (by === 'new') return b.added.localeCompare(a.added) || (a.rank - b.rank);
      if (by === 'name') return a.name.localeCompare(b.name, 'en', { sensitivity: 'base' });
      return a.rank - b.rank;
    });
    return { list: found, terms: terms };
  }

  // counts: for each filter's options, how many templates it would show.
  function counts(s, terms) {
    var c = { mode: {}, type: {}, loader: {}, version: {}, tag: {}, memory: {} };
    var add = function (m, k) { m[k] = (m[k] || 0) + 1; };
    index.list.forEach(function (t) {
      if (terms.length && !score(t, terms)) return;
      if (matches(t, s, 'mode')) t.cats.forEach(function (m) { add(c.mode, m); });
      if (matches(t, s, 'type')) add(c.type, t.kind);
      if (matches(t, s, 'loader')) add(c.loader, t.loader);
      if (matches(t, s, 'version')) add(c.version, t.version);
      if (matches(t, s)) t.tags.forEach(function (g) { add(c.tag, g); });
      if (matches(t, s, 'memory')) {
        add(c.memory, '');
        Object.keys(labels.memory || {}).forEach(function (v) { if (v && t.mb <= v * 1024) add(c.memory, v); });
      }
    });
    return c;
  }

  function gb(mb) { return (mb % 1024 === 0 ? mb / 1024 : (mb / 1024).toFixed(1)) + ' GB'; }
  function plural(n) { return n === 1 ? '1 template' : n.toLocaleString('en-US') + ' templates'; }
  function slot(el, name) { return el.querySelector('[data-slot="' + name + '"]'); }

  function card(t) {
    var d = index.data;
    var el = shell.content.firstElementChild.cloneNode(true);
    var art = d.arts[t.art];
    var img = slot(el, 'art');
    img.src = art.src;
    img.width = art.w;
    img.height = art.h;
    var name = slot(el, 'name');
    name.textContent = t.name;
    name.href = t.page;
    var l = d.loaders[t.loader];
    var logo = slot(el, 'logo');
    if (l.logo) {
      logo.src = l.logo;
      if (l.pixel) logo.classList.add('pixel');
    } else {
      logo.remove();
    }
    slot(el, 'loader').textContent = l.name + ' ' + t.version;
    slot(el, 'memory').textContent = gb(t.mb);
    slot(el, 'desc').textContent = t.desc;
    if (t.tags.indexOf('crossplay') < 0) slot(el, 'crossplay').remove();
    // What it installs, each with its icon or its initial: three, then how
    // many more.
    var installs = slot(el, 'installs');
    t.addons.slice(0, 3).forEach(function (name, i) {
      var li = document.createElement('li');
      var icon = d.icons[t.ai[i]];
      var mark = document.createElement(icon ? 'img' : 'span');
      if (icon) {
        mark.className = 'ai';
        mark.src = icon;
        mark.alt = '';
        mark.width = 18;
        mark.height = 18;
        mark.loading = 'lazy';
        mark.decoding = 'async';
      } else {
        mark.className = 'ai ai-initial';
        mark.setAttribute('aria-hidden', 'true');
        mark.textContent = name.charAt(0).toUpperCase();
      }
      var label = document.createElement('span');
      label.textContent = name;
      li.appendChild(mark);
      li.appendChild(label);
      installs.appendChild(li);
    });
    if (t.addons.length > 3) {
      var more = document.createElement('li');
      more.className = 'dcard-more';
      more.textContent = '+' + (t.addons.length - 3) + ' more';
      installs.appendChild(more);
    }
    if (!t.addons.length) installs.remove();
    var open = slot(el, 'open');
    open.href = t.open;
    open.setAttribute('data-template-open', t.id);
    slot(el, 'open-name').textContent = ' the ' + t.name + ' template';
    return el;
  }

  function label(key, value) {
    if (key === 'memory') return 'Up to ' + value + ' GB';
    if (labels[key] && labels[key][value]) return labels[key][value];
    var d = index && index.data;
    var names = d && { mode: d.cats, tag: d.tags, type: d.kinds }[key];
    if (names && names[value]) return names[value];
    if (key === 'loader' && d && d.loaders[value]) return d.loaders[value].name;
    return value;
  }

  function drawChips() {
    chips.textContent = '';
    var add = function (text, remove) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'dir-chip';
      b.innerHTML = ICON.x;
      b.insertBefore(document.createTextNode(text), b.firstChild);
      b.setAttribute('aria-label', 'Remove ' + text);
      b.addEventListener('click', function () { remove(); state.page = 1; update(); });
      chips.appendChild(b);
    };
    if (state.q) add('“' + state.q + '”', function () { state.q = ''; if (input) input.value = ''; });
    KEYS.forEach(function (k) {
      state[k].forEach(function (v) { add(label(k, v), function () { state[k] = state[k].filter(function (x) { return x !== v; }); }); });
    });
    if (state.memory) add(label('memory', state.memory), function () { state.memory = ''; });
    chips.hidden = !chips.firstChild;
    if (chips.firstChild) {
      var clear = document.createElement('button');
      clear.type = 'button';
      clear.className = 'dir-chips-clear';
      clear.textContent = 'Clear all';
      clear.addEventListener('click', clearAll);
      chips.appendChild(clear);
    }
  }

  function drawFacets(c) {
    $$('.facet', root).forEach(function (f) {
      var key = f.getAttribute('data-facet');
      $$('.facet-opt', f).forEach(function (opt) {
        var inp = $('input', opt);
        var n = c[key][inp.value] || 0;
        inp.checked = key === 'memory' ? state.memory === inp.value : state[key].indexOf(inp.value) >= 0;
        $('[data-count]', opt).textContent = n;
        opt.classList.toggle('is-empty', !n && !inp.checked);
      });
    });
    var chosen = KEYS.reduce(function (n, k) { return n + state[k].length; }, 0) + (state.memory ? 1 : 0);
    if (filterN) {
      filterN.textContent = chosen;
      filterN.hidden = !chosen;
    }
  }

  function drawSort() {
    var by = sortOf(state);
    if (sortLabel) sortLabel.textContent = SORTS[by];
    $$('.dir-sort-opt', sortMenu).forEach(function (o) {
      o.setAttribute('aria-checked', o.getAttribute('data-value') === by ? 'true' : 'false');
      if (o.getAttribute('data-value') === 'relevance') o.hidden = !state.q;
    });
  }

  function drawPages(pages) {
    pagesNav.textContent = '';
    pagesNav.hidden = pages < 2;
    if (pages < 2) return;
    var link = function (n, html, cls, rel) {
      var a = document.createElement('a');
      a.className = 'dir-page' + (cls ? ' ' + cls : '');
      a.href = address(state, n);
      a.setAttribute('data-page', n);
      if (rel) a.rel = rel;
      a.innerHTML = html;
      return a;
    };
    if (state.page > 1) pagesNav.appendChild(link(state.page - 1, ICON.left + '<span>Previous</span>', 'dir-step', 'prev'));
    var ol = document.createElement('ol');
    ol.className = 'dir-page-list';
    var last = 0;
    for (var n = 1; n <= pages; n++) {
      if (n !== 1 && n !== pages && Math.abs(n - state.page) > 1) continue;
      if (last && n - last > 1) {
        var gap = document.createElement('li');
        gap.className = 'dir-gap';
        gap.setAttribute('aria-hidden', 'true');
        gap.textContent = '…';
        ol.appendChild(gap);
      }
      var li = document.createElement('li');
      if (n === state.page) {
        var cur = document.createElement('span');
        cur.className = 'dir-page is-current';
        cur.setAttribute('aria-current', 'page');
        cur.textContent = n;
        li.appendChild(cur);
      } else {
        li.appendChild(link(n, String(n)));
      }
      ol.appendChild(li);
      last = n;
    }
    pagesNav.appendChild(ol);
    if (state.page < pages) pagesNav.appendChild(link(state.page + 1, '<span>Next</span>' + ICON.right, 'dir-step', 'next'));
  }

  // draw shows what's chosen: its cards, count, filters and pages. push adds
  // a step to the browser's history, as a new page does; typing and
  // filtering replace the current one.
  function draw(push) {
    // Best match is only there while there's a search to match.
    if (!state.q && state.sort === 'relevance') state.sort = '';
    var r = results(state);
    var total = r.list.length;
    var pages = Math.max(1, Math.ceil(total / PER_PAGE));
    state.page = Math.min(Math.max(1, state.page), pages);
    var from = (state.page - 1) * PER_PAGE;
    var frag = document.createDocumentFragment();
    r.list.slice(from, from + PER_PAGE).forEach(function (t) { frag.appendChild(card(t)); });
    grid.textContent = '';
    grid.appendChild(frag);
    grid.hidden = !total;
    empty.hidden = !!total;
    var all = index.list.length;
    countEl.textContent = (total === all ? plural(total) : total.toLocaleString('en-US') + ' of ' + plural(all)) + (pages > 1 ? ', page ' + state.page + ' of ' + pages : '');
    if (showBtn) showBtn.textContent = total ? 'Show ' + plural(total) : 'No templates match';
    drawPages(pages);
    drawChips();
    drawFacets(counts(state, r.terms));
    drawSort();
    var url = address(state, state.page);
    if (url !== location.pathname + location.search) history[push ? 'pushState' : 'replaceState'](null, '', url);
  }

  function update(push) {
    load().then(function () { draw(push); }, function () {
      countEl.textContent = 'Search and filters need the page again: reload it to try.';
    });
  }

  function clearAll() {
    var sort = state.sort;
    state = blank();
    state.sort = sort === 'relevance' ? '' : sort;
    if (input) input.value = '';
    update();
  }

  function toTop() {
    var top = root.getBoundingClientRect().top + window.scrollY - (parseInt(getComputedStyle(document.documentElement).scrollPaddingTop, 10) || 0);
    if (window.scrollY > top) window.scrollTo({ top: top, behavior: reduce.matches ? 'auto' : 'smooth' });
  }

  // Filters.
  root.addEventListener('change', function (e) {
    var inp = e.target;
    if (!inp.classList || !inp.classList.contains('facet-input')) return;
    if (inp.type === 'radio') {
      state.memory = inp.value;
    } else {
      var list = state[inp.name].filter(function (v) { return v !== inp.value; });
      if (inp.checked) list.push(inp.value);
      state[inp.name] = list;
    }
    state.page = 1;
    update();
  });
  $$('[data-facet-more]', root).forEach(function (b) {
    var more = b.textContent;
    b.addEventListener('click', function () {
      var f = b.closest('.facet');
      var open = !f.classList.contains('is-open');
      f.classList.toggle('is-open', open);
      b.setAttribute('aria-expanded', open ? 'true' : 'false');
      b.textContent = open ? 'Show fewer' : more;
    });
  });
  $$('[data-clear]', root).forEach(function (b) { b.addEventListener('click', clearAll); });

  // Pages: drawn here while a search, filter or sort is chosen; otherwise a
  // link loads the list's own page, whose heading says which it is.
  pagesNav.addEventListener('click', function (e) {
    var a = e.target.closest('a[data-page], a.dir-page');
    if (!a || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    if (!filtering(state) && sortOf(state) === 'popular') return;
    var n = parseInt(a.getAttribute('data-page') || (a.href.match(/\/page\/(\d+)/) || [])[1] || (a.href.match(/[?&]page=(\d+)/) || [])[1] || '1', 10);
    e.preventDefault();
    state.page = n;
    load().then(function () { draw(true); toTop(); });
  });

  // Search: results change as you type; Enter on a phone closes the keyboard
  // and shows them.
  if (input) {
    var timer = 0;
    input.value = state.q;
    input.addEventListener('input', function () {
      clearTimeout(timer);
      timer = setTimeout(function () {
        var q = input.value.trim().slice(0, 80);
        if (q === state.q) return;
        state.q = q;
        state.page = 1;
        update();
      }, 80);
    });
    input.addEventListener('focus', load, { once: true });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && input.value) {
        input.value = '';
        input.dispatchEvent(new Event('input'));
      } else if (e.key === 'Enter' && !wide.matches) {
        input.blur();
        root.scrollIntoView({ behavior: reduce.matches ? 'auto' : 'smooth' });
      }
    });
    document.addEventListener('keydown', function (e) {
      var t = e.target;
      if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey || (t && (t.isContentEditable || /^(input|textarea|select)$/i.test(t.tagName)))) return;
      e.preventDefault();
      input.focus();
    });
  }

  // Sort: a menu of four, one checked.
  function closeSort(focus) {
    if (sortMenu.hidden) return;
    sortMenu.hidden = true;
    sortBtn.setAttribute('aria-expanded', 'false');
    if (focus) sortBtn.focus();
  }
  function sortItems() { return $$('.dir-sort-opt', sortMenu).filter(function (o) { return !o.hidden; }); }
  if (sortBtn && sortMenu) {
    sortBtn.addEventListener('click', function () {
      if (!sortMenu.hidden) { closeSort(); return; }
      load();
      sortMenu.hidden = false;
      sortBtn.setAttribute('aria-expanded', 'true');
      var on = $('[aria-checked="true"]', sortMenu) || sortItems()[0];
      if (on) on.focus();
    });
    sortMenu.addEventListener('click', function (e) {
      var o = e.target.closest('.dir-sort-opt');
      if (!o) return;
      state.sort = o.getAttribute('data-value');
      state.page = 1;
      closeSort(true);
      update();
    });
    sortMenu.addEventListener('keydown', function (e) {
      var items = sortItems();
      var i = items.indexOf(document.activeElement);
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length].focus();
      } else if (e.key === 'Home' || e.key === 'End') {
        e.preventDefault();
        items[e.key === 'Home' ? 0 : items.length - 1].focus();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        closeSort(true);
      } else if (e.key === 'Tab') {
        closeSort();
      }
    });
    document.addEventListener('click', function (e) {
      if (!sortMenu.hidden && !e.target.closest('[data-sort]')) closeSort();
    });
  }

  // Below 1024 pixels the filters are a sheet from the bottom.
  function openSheet() {
    load();
    side.classList.add('is-open');
    scrim.hidden = false;
    filterBtn.setAttribute('aria-expanded', 'true');
    document.documentElement.classList.add('dir-locked');
    var first = $('.facet-input', side);
    setTimeout(function () { ($('[data-close-filters]', side) || first).focus(); }, reduce.matches ? 0 : 80);
  }
  function closeSheet() {
    if (!side.classList.contains('is-open')) return;
    side.classList.remove('is-open');
    scrim.hidden = true;
    filterBtn.setAttribute('aria-expanded', 'false');
    document.documentElement.classList.remove('dir-locked');
    filterBtn.focus();
  }
  if (side && filterBtn) {
    filterBtn.addEventListener('click', openSheet);
    scrim.addEventListener('click', closeSheet);
    $$('[data-close-filters]', side).forEach(function (b) { b.addEventListener('click', closeSheet); });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape') closeSheet(); });
    wide.addEventListener('change', function (e) { if (e.matches) closeSheet(); });
  }

  window.addEventListener('popstate', function () {
    state = read();
    if (input) input.value = state.q;
    load().then(function () { draw(); });
  });

  // The index loads once the page is quiet, so the first filter is instant;
  // an address with a search or filters in it is drawn from it at once.
  if (changed(state)) {
    update();
  } else {
    drawSort();
    var idle = window.requestIdleCallback || function (f) { return setTimeout(f, 1200); };
    idle(function () { load(); });
  }
})();

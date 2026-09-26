// playkeeper.io on every page: the header's scrolled look and menus, Copy,
// the FAQ's animation where the browser has none, scroll reveals, the phone
// footer's groups, a guide's contents, code tabs and the star count. Nothing
// here is needed to read or use a page; without it, everything is shown.
(function () {
  var doc = document.documentElement;
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
  var phone = window.matchMedia('(max-width: 639.98px)');
  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  // The header gets the page's colour and a line once the page scrolls.
  var header = $('[data-header]');
  if (header) {
    var scrolled = function () { header.classList.toggle('is-scrolled', window.scrollY > 80); };
    window.addEventListener('scroll', scrolled, { passive: true });
    scrolled();
  }

  // Header menus are popovers; they open under their item.
  $$('.nav-menu').forEach(function (menu) {
    var trigger = $('[popovertarget="' + menu.id + '"]');
    if (!trigger) return;
    trigger.setAttribute('aria-expanded', 'false');
    menu.addEventListener('beforetoggle', function (e) {
      if (e.newState !== 'open') return;
      var r = trigger.getBoundingClientRect();
      menu.style.top = Math.round(r.bottom + 8) + 'px';
      menu.style.left = Math.round(Math.max(12, r.left - 6)) + 'px';
    });
    menu.addEventListener('toggle', function (e) {
      trigger.setAttribute('aria-expanded', e.newState === 'open' ? 'true' : 'false');
    });
  });
  var sheet = $('#phone-menu');
  if (sheet) {
    $$('a', sheet).forEach(function (a) {
      a.addEventListener('click', function () { if (sheet.hidePopover) try { sheet.hidePopover(); } catch (err) { /* already closed */ } });
    });
    phone.addEventListener('change', function (e) { if (!e.matches && sheet.hidePopover) try { sheet.hidePopover(); } catch (err) { /* closed */ } });
  }

  // A short message at the bottom of the screen.
  var toastBox = $('[data-toast]');
  var toastTimer = 0;
  function toast(text) {
    if (!toastBox) return;
    clearTimeout(toastTimer);
    toastBox.classList.remove('is-leaving');
    toastBox.textContent = text;
    toastBox.hidden = false;
    toastTimer = setTimeout(function () {
      if (reduce.matches) { toastBox.hidden = true; return; }
      toastBox.classList.add('is-leaving');
      toastTimer = setTimeout(function () { toastBox.hidden = true; toastBox.classList.remove('is-leaving'); }, 200);
    }, 1600);
  }

  // Copy: buttons and links with data-copy copy it where the browser allows.
  if (navigator.clipboard && window.isSecureContext !== false) {
    $$('[data-copy]').forEach(function (el) {
      el.hidden = false;
      var label = $('.install-copy-label, [data-copy-label]', el);
      var before = label ? label.textContent : '';
      var timer = 0;
      el.addEventListener('click', function (e) {
        e.preventDefault();
        navigator.clipboard.writeText(el.getAttribute('data-copy')).then(function () {
          el.classList.add('is-copied');
          if (label) label.textContent = 'Copied';
          if (phone.matches || el.hasAttribute('data-copy-toast')) toast('Command copied');
          clearTimeout(timer);
          timer = setTimeout(function () {
            el.classList.remove('is-copied');
            if (label) label.textContent = before;
          }, 1600);
        }, function () {
          toast('Copying failed. Select the command and copy it instead.');
        });
      });
    });
  }

  // Code blocks in guides and the docs get their own Copy.
  if (navigator.clipboard) {
    $$('.prose pre').forEach(function (pre) {
      var code = $('code', pre);
      if (!code) return;
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'code-copy';
      b.setAttribute('data-copy', code.textContent.replace(/\n$/, ''));
      b.innerHTML = '<svg class="icon icon-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg><svg class="icon icon-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg><span data-copy-label>Copy</span>';
      pre.appendChild(b);
      b.addEventListener('click', function () {
        navigator.clipboard.writeText(b.getAttribute('data-copy')).then(function () {
          b.classList.add('is-copied');
          $('[data-copy-label]', b).textContent = 'Copied';
          setTimeout(function () { b.classList.remove('is-copied'); $('[data-copy-label]', b).textContent = 'Copy'; }, 1600);
        });
      });
    });
  }

  // Tabs in code blocks.
  $$('[role="tablist"]').forEach(function (list) {
    var tabs = $$('[role="tab"]', list);
    function select(tab, focus) {
      tabs.forEach(function (t) {
        var on = t === tab;
        t.setAttribute('aria-selected', on ? 'true' : 'false');
        t.tabIndex = on ? 0 : -1;
        var panel = document.getElementById(t.getAttribute('aria-controls'));
        if (panel) panel.hidden = !on;
      });
      var copy = $('.code-copy', list);
      var panel = document.getElementById(tab.getAttribute('aria-controls'));
      if (copy && panel) copy.setAttribute('data-copy', panel.textContent.replace(/^\s+|\s+$/g, ''));
      if (focus) tab.focus();
    }
    tabs.forEach(function (t, i) {
      t.addEventListener('click', function () { select(t); });
      t.addEventListener('keydown', function (e) {
        var n = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
        if (n) { e.preventDefault(); select(tabs[(i + n + tabs.length) % tabs.length], true); }
      });
    });
  });

  // The FAQ opens over 240 ms. Browsers that can animate <details> do it in
  // CSS; for the rest, this does, keeping one open at a time.
  var cssDetails = window.CSS && CSS.supports && CSS.supports('selector(::details-content)');
  if (!cssDetails && Element.prototype.animate) {
    $$('.faq-item').forEach(function (item) {
      var summary = $('summary', item);
      var answer = $('.faq-a', item);
      summary.addEventListener('click', function (e) {
        if (reduce.matches) return;
        e.preventDefault();
        var opening = !item.open;
        if (opening) {
          $$('.faq-item[open]', item.parentNode).forEach(function (o) { if (o !== item) o.open = false; });
          item.open = true;
          var h = answer.scrollHeight;
          answer.animate([{ height: '0px', opacity: 0 }, { height: h + 'px', opacity: 1 }], { duration: 240, easing: 'cubic-bezier(0.2, 0, 0, 1)' });
        } else {
          var a = answer.animate([{ height: answer.scrollHeight + 'px', opacity: 1 }, { height: '0px', opacity: 0 }], { duration: 240, easing: 'cubic-bezier(0.2, 0, 0, 1)' });
          a.onfinish = function () { item.open = false; };
        }
      });
    });
  }

  // Reveals: headings and cards still below the fold fade up once, when a
  // fifth of their section is in view, 60 ms apart.
  var reveals = $$('.reveal');
  if (reveals.length && 'IntersectionObserver' in window) {
    var groups = new Map();
    var fold = window.innerHeight;
    reveals.forEach(function (el) {
      if (el.getBoundingClientRect().top < fold) return;
      var box = el.closest('section, .band, .reveal-group') || el.parentNode;
      if (!groups.has(box)) groups.set(box, []);
      groups.get(box).push(el);
      el.classList.add('will-reveal');
    });
    var seen = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        var enough = entry.intersectionRatio >= 0.2 || entry.intersectionRect.height >= window.innerHeight * 0.2;
        if (!entry.isIntersecting || !enough) return;
        seen.unobserve(entry.target);
        (groups.get(entry.target) || []).forEach(function (el, i) {
          el.style.setProperty('--reveal-delay', (reduce.matches ? 0 : Math.min(i, 8) * 60) + 'ms');
          el.classList.add('is-revealed');
          el.classList.remove('will-reveal');
        });
      });
    }, { threshold: [0, 0.1, 0.2, 0.4] });
    groups.forEach(function (_, box) { seen.observe(box); });
  }

  // The closing band: Pip waves when half of it is in view.
  var closing = $('[data-closing]');
  if (closing && 'IntersectionObserver' in window) {
    var wave = new IntersectionObserver(function (entries) {
      if (entries[0].isIntersecting && !reduce.matches) {
        closing.classList.add('is-waving');
        wave.disconnect();
      }
    }, { threshold: 0.5 });
    wave.observe(closing);
  }

  // On phones the footer's link groups open and close like the FAQ.
  var cols = $$('[data-footer-col]');
  function footer() {
    cols.forEach(function (col, i) {
      var title = $('.footer-title', col);
      var list = $('ul', col);
      var button = $('button', title);
      if (phone.matches && !button) {
        button = document.createElement('button');
        button.type = 'button';
        button.setAttribute('aria-expanded', 'false');
        list.id = list.id || 'footer-links-' + i;
        button.setAttribute('aria-controls', list.id);
        button.innerHTML = '<span></span><svg class="icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>';
        button.firstChild.textContent = title.textContent;
        title.textContent = '';
        title.appendChild(button);
        list.hidden = true;
        button.addEventListener('click', function () {
          var open = button.getAttribute('aria-expanded') !== 'true';
          button.setAttribute('aria-expanded', open ? 'true' : 'false');
          list.hidden = !open;
          if (open && !reduce.matches && list.animate) list.animate([{ opacity: 0, transform: 'translateY(-4px)' }, { opacity: 1, transform: 'none' }], { duration: 240, easing: 'cubic-bezier(0.2, 0, 0, 1)' });
        });
      } else if (!phone.matches && button) {
        title.textContent = button.textContent;
        list.hidden = false;
      }
    });
  }
  if (cols.length) {
    footer();
    phone.addEventListener('change', footer);
  }

  // A guide's contents follow the section being read.
  var toc = $('[data-toc]');
  if (toc && 'IntersectionObserver' in window) {
    var links = $$('a', toc);
    var byId = {};
    links.forEach(function (a) { byId[a.getAttribute('href').slice(1)] = a; });
    var current = null;
    var mark = function (id) {
      if (current) current.classList.remove('is-current');
      current = byId[id] || null;
      if (current) current.classList.add('is-current');
    };
    var heads = links.map(function (a) { return document.getElementById(a.getAttribute('href').slice(1)); }).filter(Boolean);
    var spy = new IntersectionObserver(function () {
      var top = null;
      heads.forEach(function (h) { if (h.getBoundingClientRect().top < window.innerHeight * 0.35) top = h; });
      mark(top ? top.id : heads[0] && heads[0].id);
    }, { rootMargin: '0px 0px -60% 0px', threshold: [0, 1] });
    heads.forEach(function (h) { spy.observe(h); });
    window.addEventListener('scroll', function () {
      var top = null;
      heads.forEach(function (h) { if (h.getBoundingClientRect().top < window.innerHeight * 0.35) top = h; });
      mark(top ? top.id : heads[0] && heads[0].id);
    }, { passive: true });
    if (heads[0]) mark(heads[0].id);
  }

  // Star on GitHub shows the count once there are enough stars to show.
  var star = $('[data-stars]');
  if (star && window.fetch) {
    var from = Number(star.getAttribute('data-stars')) || 50;
    var show = function (n) {
      if (!(n >= from)) return;
      var el = $('[data-star-count]', star);
      el.textContent = n >= 1000 ? (Math.round(n / 100) / 10) + 'k' : String(n);
      el.hidden = false;
      star.setAttribute('aria-label', 'Star on GitHub, ' + n + ' stars');
    };
    var cached = null;
    try { cached = JSON.parse(sessionStorage.getItem('playkeeper.stars') || 'null'); } catch (err) { cached = null; }
    if (cached && Date.now() - cached.at < 3600000) {
      show(cached.n);
    } else {
      fetch('https://api.github.com/repos/CIYAhq/playkeeper', { headers: { Accept: 'application/vnd.github+json' }, credentials: 'omit', referrerPolicy: 'no-referrer' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (repo) {
          if (!repo || typeof repo.stargazers_count !== 'number') return;
          try { sessionStorage.setItem('playkeeper.stars', JSON.stringify({ n: repo.stargazers_count, at: Date.now() })); } catch (err) { /* private mode */ }
          show(repo.stargazers_count);
        })
        .catch(function () { /* the button works without a count */ });
    }
  }

  doc.classList.add('has-js');
})();

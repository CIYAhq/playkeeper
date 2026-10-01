// playkeeper.io on every page: the header's scrolled look and menus, the
// analytics' custom events, the landing page's install command for the
// channel a visitor came from, Copy and Send to my computer, the FAQ's
// animation where the browser has none, scroll reveals, a film that plays
// while it's in view, the phone footer's groups, a guide's contents, code
// tabs, the star count, and Open in my dashboard on template links. Nothing
// here is needed to read or use a page; without it, everything is shown, and
// template links go to the share page.
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

  // A short message at the bottom of the screen, for 1.6 s or ms.
  var toastBox = $('[data-toast]');
  var toastTimer = 0;
  function toast(text, ms) {
    if (!toastBox) return;
    clearTimeout(toastTimer);
    toastBox.classList.remove('is-leaving');
    toastBox.textContent = text;
    toastBox.hidden = false;
    toastTimer = setTimeout(function () {
      if (reduce.matches) { toastBox.hidden = true; return; }
      toastBox.classList.add('is-leaving');
      toastTimer = setTimeout(function () { toastBox.hidden = true; toastBox.classList.remove('is-leaving'); }, 200);
    }, ms || 1600);
  }

  // Custom events for the analytics (Settings.Analytics), which its funnels
  // are built from, each with the page it happened on. oa.js loads async, so
  // an event before it has loaded waits for it. Pages without the analytics,
  // like the share page /t, send nothing.
  var counter = $('script[data-collector]');
  var waiting = [];
  var tracker = function () { return window.oa && typeof window.oa.track === 'function' ? window.oa : null; };
  // leaving: the event's link leaves the page, so it goes at once rather than
  // with oa.js's next batch, which a page on its way out can miss.
  function count(name, props, leaving) {
    props.where = location.pathname;
    if (!counter) return;
    var oa = tracker();
    if (!oa) { waiting.push([name, props]); return; }
    oa.track(name, props);
    if (leaving && oa.flush) oa.flush();
  }
  if (counter && !tracker()) {
    counter.addEventListener('load', function () {
      var oa = tracker();
      if (oa) waiting.splice(0).forEach(function (w) { oa.track(w[0], w[1]); });
    });
  }

  // The stats service (Settings.Stats) counts copies of the install command
  // for its funnel: the channel's code and nothing else, once a page view,
  // with no cookie or referrer, and none when the browser asks not to be
  // tracked. It takes counts only from this site's origin, which a CORS
  // request always names (a no-cors one without a referrer names none), and
  // uses the address a count comes from for a rate limit and drops it.
  var stats = $('meta[name="playkeeper-stats"]');
  var told = false;
  function tell(channel) {
    if (!stats || told || !window.fetch || navigator.globalPrivacyControl || navigator.doNotTrack === '1') return;
    told = true;
    fetch(stats.content + '/v1/site', {
      method: 'POST', mode: 'cors', credentials: 'omit', referrerPolicy: 'no-referrer', keepalive: true,
      headers: { 'Content-Type': 'text/plain' },
      body: JSON.stringify({ event: 'install_copied', channel: channel || '' })
    }).catch(function () { /* a count that doesn't arrive is lost, and nothing else */ });
  }

  // The install command, the site's or the GitHub release's, with or without
  // options, copied with a Copy (el) or by hand; a channel's, /install/<code>,
  // says which.
  var installCommand = /(playkeeper\.io\/install|releases\/latest\/download\/get\.sh)[^|]*\|\s*sudo\s+sh/;
  function copied(text, el) {
    if (!installCommand.test(text)) return;
    var props = { spot: !el ? 'selection' : el.closest('[data-closing]') ? 'closing' : el.closest('[data-install]') ? 'box' : el.closest('pre, .codeblock') ? 'code' : 'button' };
    var tag = /playkeeper\.io\/install\/([a-z0-9-]+)/i.exec(text);
    if (tag) props.channel = tag[1].toLowerCase();
    count('install_copied', props);
    tell(props.channel);
  }

  // Links out: to the repository on GitHub, or /community, which sends people
  // to its Discussions; Watch releases; to a VPS provider; and to the live
  // demo. A middle-click opens one too.
  function followed(e) {
    if (e.type === 'auxclick' && e.button !== 1) return;
    var a = e.target.closest && e.target.closest('a[href]');
    if (!a) return;
    var repo = a.hostname === 'github.com' && /^\/CIYAhq\/playkeeper(\/|$)/.test(a.pathname);
    if (repo || (a.origin === location.origin && a.pathname === '/community')) {
      var part = repo ? a.pathname.split('/')[3] || 'repo' : 'community';
      count('github_clicked', { link: part === 'blob' || part === 'tree' ? 'file' : part }, true);
    }
    if (a.hasAttribute('data-watch-releases')) count('watch_releases_clicked', { plan: a.getAttribute('data-watch-releases') }, true);
    if (a.hasAttribute('data-template-open')) {
      count('template_opened', { template: a.getAttribute('data-template-open'), spot: a.closest('.tpage-actions') ? 'page' : a.closest('.tpage-related') ? 'related' : 'card' }, true);
    }
    var provider = a.origin !== location.origin && a.closest('[data-provider]');
    if (provider) count('provider_clicked', { provider: provider.getAttribute('data-provider'), plan: $('[data-plan]', provider).textContent }, true);
    if (a.origin === location.origin && /^\/demo(\/|$)/.test(a.pathname)) {
      count('demo_opened', { spot: a.closest('#phone-menu') ? 'menu' : a.closest('[data-header]') ? 'header' : a.closest('[data-closing]') ? 'closing' : 'page' }, true);
    }
  }
  document.addEventListener('copy', function () { copied(String(document.getSelection()), null); });
  document.addEventListener('click', followed, true);
  document.addEventListener('auxclick', followed, true);

  // The landing page shows a visitor who came through a channel's link
  // (/go/<code>, which adds utm_content=<code>) that channel's install
  // command, /install/<code>, for the codes it lists. It's read from the
  // address; nothing is stored.
  var listed = $('[data-channels]');
  var channel = listed && new URLSearchParams(location.search).get('utm_content');
  if (channel && listed.getAttribute('data-channels').split(' ').indexOf(channel) !== -1) {
    var tagged = function (s) { return s.replace(/(playkeeper\.io\/install)\b(?!\/)/g, '$1/' + channel); };
    $$('[data-install], [data-terminal]').forEach(function (box) {
      if (box.hasAttribute('data-install')) box.classList.add('install-tagged');
      var walk = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
      for (var t = walk.nextNode(); t; t = walk.nextNode()) t.nodeValue = tagged(t.nodeValue);
      $$('[data-copy]', box).forEach(function (el) { el.setAttribute('data-copy', tagged(el.getAttribute('data-copy'))); });
    });
  }

  // Copy: buttons and links with data-copy copy it where the browser allows.
  var canCopy = !!navigator.clipboard && window.isSecureContext !== false;
  if (canCopy) {
    $$('[data-copy]').forEach(function (el) {
      el.hidden = false;
      var label = $('.install-copy-label, [data-copy-label]', el);
      var before = label ? label.textContent : '';
      var timer = 0;
      el.addEventListener('click', function (e) {
        e.preventDefault();
        navigator.clipboard.writeText(el.getAttribute('data-copy')).then(function () {
          copied(el.getAttribute('data-copy'), el);
          el.classList.add('is-copied');
          if (label) label.textContent = 'Copied';
          if (phone.matches || el.hasAttribute('data-copy-toast')) toast(el.getAttribute('data-copy-toast') || 'Command copied');
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

  // Send to my computer, on phones (a page's share setting): the phone's
  // share sheet with the page's address as it is, ad IDs and all, or where
  // the browser has none or it fails, the address copied.
  $$('[data-share]').forEach(function (el) {
    if (!navigator.share && !canCopy) return;
    el.hidden = false;
    var label = $('span', el);
    var before = label.textContent;
    var timer = 0;
    var props = function (how) { return { spot: el.closest('[data-closing]') ? 'closing' : 'box', how: how }; };
    var failed = function () { toast("Sharing failed. Copy this page's address instead."); };
    var copyLink = function () {
      if (!canCopy) { failed(); return; }
      navigator.clipboard.writeText(location.href).then(function () {
        count('install_shared', props('copy'), true);
        label.textContent = 'Link copied';
        toast('Link copied. Send it to yourself and open it on your computer.', 3000);
        clearTimeout(timer);
        timer = setTimeout(function () { label.textContent = before; }, 1600);
      }, failed);
    };
    el.addEventListener('click', function () {
      if (!navigator.share) { copyLink(); return; }
      navigator.share({ title: document.title, url: location.href }).then(function () {
        count('install_shared', props('share'), true);
      }, function (err) {
        if (!err || err.name !== 'AbortError') copyLink();
      });
    });
  });

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
          copied(b.getAttribute('data-copy'), b);
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
      var copy = $('.code-copy', list.parentNode);
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
    var shown = tabs.filter(function (t) { return t.getAttribute('aria-selected') === 'true'; })[0] || tabs[0];
    if (shown) select(shown);
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

  // A film (a page's film setting) plays, muted, while half of it is in
  // view, unless the visitor asks for less motion; its controls play it
  // either way.
  $$('[data-film]').forEach(function (film) {
    if (!('IntersectionObserver' in window) || reduce.matches) return;
    new IntersectionObserver(function (entries) {
      if (!entries[entries.length - 1].isIntersecting) { film.pause(); return; }
      var playing = film.play();
      if (playing) playing.catch(function () { /* autoplay refused: its controls still play it */ });
    }, { threshold: 0.5 }).observe(film);
  });

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

  // Open in my dashboard. The visitor's dashboard address stays in this
  // browser (localStorage) and is never sent anywhere: once it's known, a
  // template's Open goes straight to that dashboard's New server with the
  // template, which shows everything and installs nothing until confirmed.
  // The first time, a dialog asks where the dashboard is. A bare name like
  // alex is alex.playkeeper.me, and an address without https:// gets
  // Playkeeper's port, 8443, unless it has one. The share page (js/t.js)
  // reads and saves the same address, and takes one from the dashboard's
  // Browse templates link, /t#dashboard=<address>, where no analytics runs.
  var DASHBOARD = 'playkeeper.dashboard';
  // A free name, by names.CheckName's rules.
  var reFreeName = /^(?=.{3,32}$)[a-z0-9]+(?:-[a-z0-9]+)*$/;
  // A link to the share page with a template in it.
  var reShared = /^\/t#(?:template=)?([A-Za-z0-9_-]+)$/;

  // parseDashboard turns what someone typed into their dashboard's origin,
  // or '' when it isn't an HTTPS address.
  function parseDashboard(value) {
    var typed = String(value || '').replace(/\s+/g, '');
    if (!typed) return '';
    if (reFreeName.test(typed.toLowerCase())) typed = typed.toLowerCase() + '.playkeeper.me';
    var bare = !/^[a-z][a-z0-9+.-]*:\/\//i.test(typed);
    var u;
    try {
      u = new URL(bare ? 'https://' + typed : typed);
    } catch (err) {
      return '';
    }
    // playkeeper.io is never a dashboard, even from the live demo.
    if (u.protocol !== 'https:' || !u.hostname || u.username || u.password || u.host === location.host) return '';
    // A server's own address, like survival.alex.playkeeper.me, is on the
    // machine whose dashboard is at alex.playkeeper.me.
    var server = /^(?:[a-z0-9-]+\.)+([a-z0-9-]+\.playkeeper\.me)$/.exec(u.hostname);
    if (server) u.hostname = server[1];
    if (bare && !u.port && !/^[^\/?#]*:443(?:[\/?#]|$)/.test(typed)) u.port = '8443';
    return u.origin;
  }
  function savedDashboard() {
    try {
      return parseDashboard(localStorage.getItem(DASHBOARD));
    } catch (err) {
      return '';
    }
  }
  function saveDashboard(origin) {
    try {
      if (origin) localStorage.setItem(DASHBOARD, origin);
      else localStorage.removeItem(DASHBOARD);
    } catch (err) {
      // Storage is off in this browser: the dialog asks again next time.
    }
    pointTemplates();
  }
  function dashboardURL(origin, template) { return origin + '/servers/new#template=' + template; }
  // hostOf is how an origin is shown: without https:// or the default port.
  function hostOf(origin) { return origin.replace(/^https:\/\//, '').replace(/:8443$/, ''); }
  // typedOf is an origin as a field holds it: as short as it can be while
  // parseDashboard reads it back the same, so port 443 keeps its https://.
  function typedOf(origin) { return /:\d+$/.test(origin) ? hostOf(origin) : origin; }

  // pointTemplates points every link with a template at the dashboard once
  // it's known, so a middle-click or a copied link goes there too, and shows
  // where templates open.
  function pointTemplates() {
    var origin = savedDashboard();
    $$('a[href^="/t#"], a[data-template]').forEach(function (a) {
      if (a.closest('.dash-dialog')) return;
      var m = reShared.exec(a.getAttribute('href'));
      var template = a.getAttribute('data-template') || (m && m[1]);
      if (!template) return;
      a.setAttribute('data-template', template);
      a.setAttribute('href', origin ? dashboardURL(origin, template) : '/t#' + template);
    });
    $$('[data-dashboard-line]').forEach(function (line) {
      $('[data-dashboard-host]', line).textContent = hostOf(origin);
      line.hidden = !origin;
    });
  }

  // openTemplate sends the visitor to origin with link's template. A card
  // js/templates.js drew links /t/<id>, and that script finds its template
  // (dashboard.find); without it, the link is followed.
  function openTemplate(origin, link) {
    var template = link.getAttribute('data-template');
    if (template) {
      location.assign(dashboardURL(origin, template));
      return;
    }
    var follow = function () { location.assign(link.href); };
    var find = window.playkeeperSite.dashboard.find;
    if (!find) return follow();
    find(link).then(function (t) {
      if (t) location.assign(dashboardURL(origin, t));
      else follow();
    }, follow);
  }

  // The dialog asks where the dashboard is: before opening a template
  // (link), or to change or forget the one saved (link null).
  var dialog = null;
  function dashboardDialog() {
    var d = document.createElement('dialog');
    d.className = 'dash-dialog';
    d.setAttribute('aria-labelledby', 'dash-title');
    d.innerHTML = '<div class="dash-body">' +
      '<h2 class="dash-title" id="dash-title"></h2>' +
      '<p class="dash-text">Type your free name, like <b>alex</b> for alex.playkeeper.me, or the address you open your dashboard at.</p>' +
      '<label class="dash-label" for="dash-input">Your dashboard</label>' +
      '<input class="dash-input" id="dash-input" type="text" inputmode="url" autocomplete="url" autocapitalize="off" spellcheck="false" placeholder="alex">' +
      '<p class="dash-status" role="status"></p>' +
      '<p class="dash-note">This browser remembers it, and it never leaves your browser.</p>' +
      '<div class="dash-actions">' +
      '<button type="button" class="btn btn-outline dash-forget" data-dash-forget>Forget it</button>' +
      '<button type="button" class="btn btn-outline" data-dash-cancel>Cancel</button>' +
      '<button type="button" class="btn btn-primary" data-dash-go></button>' +
      '</div></div>' +
      '<p class="dash-foot">No dashboard yet? <a data-dash-share href="/t">See what the template sets up</a> and how to get one.</p>';
    document.body.appendChild(d);
    var input = $('.dash-input', d);
    var go = function () {
      var origin = parseDashboard(input.value);
      if (!origin) {
        $('.dash-status', d).textContent = 'Type your name, like alex, or your dashboard\u2019s address, like 203.0.113.7.';
        input.focus();
        return;
      }
      var link = d.link;
      saveDashboard(origin);
      d.close();
      if (link) openTemplate(origin, link);
      else toast('Templates open in ' + hostOf(origin), 2400);
    };
    $('[data-dash-go]', d).addEventListener('click', go);
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); go(); }
    });
    $('[data-dash-cancel]', d).addEventListener('click', function () { d.close(); });
    $('[data-dash-forget]', d).addEventListener('click', function () {
      saveDashboard('');
      d.close();
      toast('Forgotten. Templates ask where your dashboard is again.', 2400);
    });
    return d;
  }
  function askDashboard(link) {
    if (!window.HTMLDialogElement) {
      if (link) location.assign(link.getAttribute('data-template') ? '/t#' + link.getAttribute('data-template') : link.href);
      return;
    }
    dialog = dialog || dashboardDialog();
    var saved = savedDashboard();
    dialog.link = link;
    $('.dash-title', dialog).textContent = link ? 'Where\u2019s your dashboard?' : 'Your dashboard';
    $('[data-dash-go]', dialog).textContent = link ? 'Open' : 'Save';
    $('[data-dash-forget]', dialog).hidden = !saved || !!link;
    $('.dash-foot', dialog).hidden = !link;
    if (link) $('[data-dash-share]', dialog).setAttribute('href', link.getAttribute('data-template') ? '/t#' + link.getAttribute('data-template') : link.getAttribute('href'));
    $('.dash-status', dialog).textContent = '';
    $('.dash-input', dialog).value = saved ? typedOf(saved) : '';
    dialog.showModal();
    $('.dash-input', dialog).focus();
  }

  document.addEventListener('click', function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    var change = e.target.closest && e.target.closest('[data-dashboard-change]');
    if (change) {
      askDashboard(null);
      return;
    }
    var link = e.target.closest && e.target.closest('a[data-template], a[href^="/t/"]');
    if (!link || link.closest('[data-share-page], .dash-dialog')) return;
    e.preventDefault();
    var origin = savedDashboard();
    if (origin) openTemplate(origin, link);
    else askDashboard(link);
  });
  window.addEventListener('storage', function (e) { if (e.key === DASHBOARD) pointTemplates(); });
  pointTemplates();

  // A page's own script, like a free tool's, counts its events and shows the
  // toast through these, and the share page keeps the dashboard's address.
  // dashboard.find, which the directory's script sets, finds the template a
  // /t/<id> link opens.
  window.playkeeperSite = {
    toast: toast,
    count: function (name, props) { count(name, props || {}); },
    dashboard: { parse: parseDashboard, saved: savedDashboard, save: saveDashboard, url: dashboardURL, host: hostOf, typed: typedOf, find: null },
  };

  doc.classList.add('has-js');
})();

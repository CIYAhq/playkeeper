// The landing page's motion, as the design's motion spec has it: the
// product loop (New server, setting up, then the Overview, where the cursor
// copies the join address), its toasts one at a time, Pip hopping onto the
// window, waving and ducking from the pointer, the terminal typing its
// command once, and the demo window's depth on scroll. The loop pauses off
// screen. With reduced motion, or without this script, the page shows the
// design's still: the Overview with both toasts.
(function () {
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };
  if (reduce.matches) return;

  var product = $('[data-product]');
  var loop = $('[data-loop]');
  var pip = $('[data-pip]');
  var moments = { copied: $('[data-moment="copied"]'), joined: $('[data-moment="joined"]') };

  // Toasts slide 12 px in from the right (320 ms), stay 2.4 s and fade
  // out (200 ms), one at a time.
  var shownMoment = null;
  function moment(name) {
    var el = moments[name];
    if (!el || getComputedStyle(el).display === 'none') return;
    if (shownMoment && shownMoment !== el) shownMoment.classList.remove('is-in');
    shownMoment = el;
    el.classList.remove('is-out');
    el.classList.add('is-in');
    later(function () {
      el.classList.add('is-out');
      el.classList.remove('is-in');
    }, 2720);
  }

  // The loop's timers, cleared when it pauses.
  var timers = [];
  function later(fn, ms) { timers.push(setTimeout(fn, ms)); }
  function stop() { timers.forEach(clearTimeout); timers = []; }

  var frames = loop ? $$('.loop-frame', loop) : [];
  var cursor = loop ? $('.loop-cursor', loop) : null;
  function frame(i) {
    frames.forEach(function (f, n) { f.classList.toggle('is-on', n === i); });
    if (loop) loop.setAttribute('data-frame', String(i + 1));
  }

  // One run of the 9 s loop: pick a type, setting up as its steps check off,
  // then the Overview, where the cursor presses Copy join address.
  function run() {
    stop();
    frame(0);
    if (cursor) cursor.className = 'loop-cursor';
    later(function () { moment('joined'); }, 300);
    later(function () { if (cursor) cursor.classList.add('to-type'); }, 700);
    later(function () { if (cursor) cursor.classList.add('press'); }, 1700);
    later(function () { if (cursor) cursor.classList.remove('press'); frame(1); if (cursor) cursor.className = 'loop-cursor away'; }, 2600);
    later(function () { frame(2); }, 3900);
    later(function () { frame(3); }, 5200);
    later(function () { if (cursor) cursor.className = 'loop-cursor to-copy'; }, 5700);
    later(function () { if (cursor) cursor.classList.add('press'); }, 6700);
    later(function () { if (cursor) cursor.classList.remove('press'); moment('copied'); }, 6850);
    later(function () { if (cursor) cursor.className = 'loop-cursor away'; }, 8200);
    later(run, 9000);
  }

  if (product) {
    product.classList.add('is-moving');
    Object.keys(moments).forEach(function (k) { if (moments[k]) moments[k].classList.add('is-out'); });
    if ('IntersectionObserver' in window) {
      new IntersectionObserver(function (entries) {
        if (entries[0].isIntersecting) run();
        else stop();
      }, { threshold: 0.15 }).observe(product);
    } else {
      run();
    }
  }

  // Pip hops onto the window once it lands, waves every 8 s, and ducks when
  // the pointer comes within 40 px.
  if (pip) {
    pip.classList.add('is-landing');
    setTimeout(function () { pip.classList.remove('is-landing'); pip.classList.add('is-landed'); }, 760);
    setInterval(function () {
      if (document.hidden || pip.classList.contains('is-ducking')) return;
      pip.classList.remove('is-waving');
      void pip.offsetWidth;
      pip.classList.add('is-waving');
    }, 8000);
    var near = false;
    document.addEventListener('pointermove', function (e) {
      var r = pip.getBoundingClientRect();
      var dx = Math.max(r.left - e.clientX, 0, e.clientX - r.right);
      var dy = Math.max(r.top - e.clientY, 0, e.clientY - r.bottom);
      var close = Math.sqrt(dx * dx + dy * dy) < 40;
      if (close !== near) {
        near = close;
        pip.classList.toggle('is-ducking', close);
      }
    }, { passive: true });
  }

  // The terminal types its command at 30 characters a second, then prints
  // each line 140 ms apart, once, when it comes into view.
  $$('[data-terminal]').forEach(function (term) {
    var cmd = $('[data-type]', term);
    if (!cmd || !('IntersectionObserver' in window)) return;
    var prompt = $('.t-prompt', cmd);
    var text = cmd.textContent.replace(/^\$ /, '');
    var lines = $$('[data-line]', term);
    var io = new IntersectionObserver(function (entries) {
      if (!entries[0].isIntersecting) return;
      io.disconnect();
      term.classList.add('is-typing');
      cmd.textContent = '';
      if (prompt) cmd.appendChild(prompt);
      var typed = document.createTextNode('');
      cmd.appendChild(typed);
      cmd.classList.add('caret');
      var i = 0;
      var tick = setInterval(function () {
        i++;
        typed.textContent = text.slice(0, i);
        if (i < text.length) return;
        clearInterval(tick);
        cmd.classList.remove('caret');
        lines.forEach(function (line, n) {
          setTimeout(function () { line.classList.add('is-shown'); }, 140 * (n + 1));
        });
      }, 1000 / 30);
    }, { threshold: 0.6 });
    io.observe(term);
  });

  // The demo window moves at 0.9× the scroll speed.
  var windowEl = $('[data-parallax]');
  if (windowEl && window.matchMedia('(min-width: 1024px)').matches) {
    var ticking = false;
    var place = function () {
      ticking = false;
      var r = windowEl.parentNode.getBoundingClientRect();
      var offset = (r.top + r.height / 2 - window.innerHeight / 2) * 0.1;
      windowEl.style.transform = 'translate3d(0,' + offset.toFixed(1) + 'px,0)';
    };
    window.addEventListener('scroll', function () {
      if (!ticking) { ticking = true; requestAnimationFrame(place); }
    }, { passive: true });
    place();
  }
})();

// The sizing guide's answers: shows the answer from sizing-data.js for the
// chosen friends and software, keeps the choice in the address
// (#friends=5-10&run=vanilla), and on phones opens "What will you run?" as a
// sheet and the site links as a menu.
(function () {
  var data = window.playkeeperSizing;
  if (!data) return;
  var byId = function (id) { return document.getElementById(id); };
  var phone = window.matchMedia('(max-width: 640px)');
  var run = byId('run'), runButton = byId('run-button'), options = byId('run-options'), sheetClose = byId('sheet-close');
  var menuButton = byId('menu-button'), nav = byId('site-nav'), backdrop = byId('backdrop');
  var reasons = document.querySelectorAll('.reason');
  var current = null;
  // The address of the answer on show, like #friends=5-10&run=vanilla.
  var shown = '';
  // The software checked when the sheet opened, which closing it without
  // picking puts back.
  var before = null;
  // Whether the key being handled is an arrow key: moving through the options
  // with arrow keys also clicks them, and only other clicks pick and close.
  var arrowing = false;

  function radios(name) { return document.querySelectorAll('input[name="' + name + '"]'); }
  function checked(name) {
    var inputs = radios(name);
    for (var i = 0; i < inputs.length; i++) if (inputs[i].checked) return inputs[i];
    return null;
  }
  function pick(name, value) {
    var inputs = radios(name);
    for (var i = 0; i < inputs.length; i++) if (inputs[i].value === value) inputs[i].checked = true;
  }
  function sheetOpen() { return run.classList.contains('open'); }

  function show(announce) {
    var friends = checked('friends'), software = checked('run');
    if (!friends || !software) return;
    var answer = (data.answers[friends.value] || {})[software.value];
    if (!answer) return;
    byId('answer-long').textContent = answer.title;
    byId('answer-short').textContent = answer.short;
    byId('answer-summary').textContent = answer.summary;
    byId('answer-specs').textContent = answer.specs;
    for (var i = 0; i < reasons.length && i < answer.reasons.length; i++) {
      reasons[i].querySelector('strong').textContent = answer.reasons[i].value;
      reasons[i].querySelector('.reason-text').textContent = answer.reasons[i].text;
    }
    byId('run-current').textContent = document.querySelector('label[for="' + software.id + '"] .name').textContent;
    if (current) current.classList.remove('current');
    current = byId('size-' + friends.value + '-' + software.value);
    if (current) current.classList.add('current');
    if (announce) byId('answer-status').textContent = answer.announce;
    shown = '#friends=' + friends.value + '&run=' + software.value;
  }
  // choose shows and remembers the answer for what is checked, unless it is
  // already on show.
  function choose() {
    var friends = checked('friends'), software = checked('run');
    if (!friends || !software || '#friends=' + friends.value + '&run=' + software.value === shown) return;
    show(true);
    history.replaceState(null, '', shown);
  }

  function fromAddress() {
    var params = new URLSearchParams(location.hash.slice(1));
    if (params.has('friends')) pick('friends', params.get('friends'));
    if (params.has('run')) pick('run', params.get('run'));
  }

  function openSheet() {
    before = checked('run');
    run.classList.add('open');
    runButton.setAttribute('aria-expanded', 'true');
    options.setAttribute('role', 'dialog');
    options.setAttribute('aria-modal', 'true');
    options.setAttribute('aria-labelledby', 'sheet-title');
    backdrop.hidden = false;
    (before || radios('run')[0]).focus();
  }
  // closeSheet keeps the software picked in the sheet, or puts back the one
  // checked when it opened.
  function closeSheet(keep, returnFocus) {
    if (!sheetOpen()) return;
    run.classList.remove('open');
    runButton.setAttribute('aria-expanded', 'false');
    options.removeAttribute('role');
    options.removeAttribute('aria-modal');
    options.removeAttribute('aria-labelledby');
    backdrop.hidden = true;
    if (!keep && before) before.checked = true;
    before = null;
    choose();
    if (returnFocus) runButton.focus();
  }
  function closeMenu(returnFocus) {
    if (!nav.classList.contains('open')) return;
    nav.classList.remove('open');
    menuButton.setAttribute('aria-expanded', 'false');
    if (returnFocus) menuButton.focus();
  }

  document.addEventListener('change', function (e) {
    if (e.target.name !== 'friends' && e.target.name !== 'run') return;
    if (e.target.name === 'run' && sheetOpen()) return;
    choose();
  });
  window.addEventListener('hashchange', function () {
    fromAddress();
    show(true);
  });

  document.addEventListener('pointerdown', function () { arrowing = false; }, true);
  document.addEventListener('keydown', function (e) {
    arrowing = /^Arrow/.test(e.key);
    if (arrowing) setTimeout(function () { arrowing = false; });
    if (e.key === 'Escape') {
      closeSheet(false, true);
      closeMenu(true);
    }
    // The sheet has two stops, the close button and the options.
    if (e.key === 'Tab' && sheetOpen()) {
      e.preventDefault();
      (document.activeElement === sheetClose ? checked('run') || radios('run')[0] : sheetClose).focus();
    }
  }, true);
  runButton.addEventListener('click', function () {
    if (sheetOpen()) closeSheet(false, true);
    else openSheet();
  });
  options.addEventListener('keydown', function (e) {
    if ((e.key === 'Enter' || e.key === ' ') && e.target.name === 'run' && sheetOpen()) {
      e.preventDefault();
      closeSheet(true, true);
    }
  });
  options.addEventListener('click', function (e) {
    if (e.target.name === 'run' && !arrowing) closeSheet(true, true);
  });
  options.addEventListener('focusout', function (e) {
    if (e.relatedTarget && !options.contains(e.relatedTarget)) closeSheet(false, false);
  });
  sheetClose.addEventListener('click', function () { closeSheet(false, true); });
  backdrop.addEventListener('click', function () { closeSheet(false, true); });

  menuButton.addEventListener('click', function () {
    var open = nav.classList.toggle('open');
    menuButton.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
  document.addEventListener('click', function (e) {
    if (!nav.contains(e.target) && !menuButton.contains(e.target)) closeMenu(false);
  });
  phone.addEventListener('change', function () {
    closeSheet(false, false);
    closeMenu(false);
  });

  fromAddress();
  show(false);
})();

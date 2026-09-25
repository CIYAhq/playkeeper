// The sizing guide's answers: shows the answer from sizing-data.js for the
// chosen friends and software, keeps the choice in the address
// (#friends=5-10&run=vanilla), and on phones opens "What will you run?" as a
// sheet and the site links as a menu.
(function () {
  var data = window.playkeeperSizing;
  if (!data) return;
  var byId = function (id) { return document.getElementById(id); };
  var phone = window.matchMedia('(max-width: 640px)');
  var run = byId('run'), runButton = byId('run-button'), options = byId('run-options');
  var menuButton = byId('menu-button'), nav = byId('site-nav'), backdrop = byId('backdrop');
  var reasons = document.querySelectorAll('.reason');
  var current = null;
  // Whether the last press was a pointer on the sheet or its backdrop, not a
  // key: a tap picks an option and closes the sheet, arrow keys only move
  // through the options.
  var tapping = false;

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
  }

  function fromAddress() {
    var params = new URLSearchParams(location.hash.slice(1));
    if (params.has('friends')) pick('friends', params.get('friends'));
    if (params.has('run')) pick('run', params.get('run'));
  }

  function openSheet() {
    run.classList.add('open');
    runButton.setAttribute('aria-expanded', 'true');
    backdrop.hidden = false;
    var software = checked('run');
    if (software) software.focus();
  }
  function closeSheet(returnFocus) {
    if (!run.classList.contains('open')) return;
    run.classList.remove('open');
    runButton.setAttribute('aria-expanded', 'false');
    backdrop.hidden = true;
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
    show(true);
    history.replaceState(null, '', '#friends=' + checked('friends').value + '&run=' + checked('run').value);
  });
  window.addEventListener('hashchange', function () {
    fromAddress();
    show(true);
  });

  document.addEventListener('pointerdown', function (e) { tapping = options.contains(e.target) || e.target === backdrop; }, true);
  document.addEventListener('keydown', function (e) {
    tapping = false;
    if (e.key === 'Escape') {
      closeSheet(true);
      closeMenu(true);
    }
  }, true);
  runButton.addEventListener('click', function () {
    if (run.classList.contains('open')) closeSheet(true);
    else openSheet();
  });
  options.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && run.classList.contains('open')) {
      e.preventDefault();
      closeSheet(true);
    }
  });
  options.addEventListener('click', function (e) {
    if (e.target.name === 'run' && tapping) closeSheet(true);
  });
  options.addEventListener('focusout', function (e) {
    if (!tapping && !options.contains(e.relatedTarget)) closeSheet(false);
  });
  backdrop.addEventListener('click', function () { closeSheet(true); });

  menuButton.addEventListener('click', function () {
    var open = nav.classList.toggle('open');
    menuButton.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
  document.addEventListener('click', function (e) {
    if (!nav.contains(e.target) && !menuButton.contains(e.target)) closeMenu(false);
  });
  phone.addEventListener('change', function () {
    closeSheet(false);
    closeMenu(false);
  });

  fromAddress();
  show(false);
})();

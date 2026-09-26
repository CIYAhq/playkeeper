// The sizing guide's calculator: shows the answer from sizing-data.js for the
// chosen friends and software, marks it in the table of every size, fits the
// providers' plans to it, keeps the choice in the address on /sizing
// (#friends=5-10&run=vanilla), and on phones opens "What will you run?" as a
// sheet.
(function () {
  var data = window.playkeeperSizing;
  var root = document.querySelector('[data-sizing]');
  if (!data || !root) return;
  var byId = function (id) { return document.getElementById(id); };
  var phone = window.matchMedia('(max-width: 640px)');
  var compact = root.hasAttribute('data-sizing-compact');
  var run = byId('run'), runButton = byId('run-button'), options = byId('run-options'), sheetClose = byId('sheet-close');
  var backdrop = byId('backdrop');
  var reasons = root.querySelectorAll('.reason');
  var current = null;
  // The address of the answer on show, like #friends=5-10&run=vanilla.
  var shown = '';
  // The software checked when the sheet opened, which closing it without
  // picking puts back.
  var before = null;
  // Whether the key being handled is an arrow key: moving through the options
  // with arrow keys also clicks them, and only other clicks pick and close.
  var arrowing = false;

  function radios(name) { return root.querySelectorAll('input[name="' + name + '"]'); }
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

  // fit is the smallest plan with the memory and cores, as Provider.Fit.
  function fit(plans, memoryGB, cores) {
    for (var i = 0; i < plans.length; i++) {
      if (plans[i].memoryGB >= memoryGB && plans[i].cpus >= cores) return plans[i];
    }
    return null;
  }
  function showPlans(answer) {
    var label = document.querySelector('[data-providers] [data-fit-label]');
    if (label) label.textContent = answer.fit;
    var cards = document.querySelectorAll('[data-providers] [data-provider]');
    for (var i = 0; i < cards.length; i++) {
      var plan = fit((data.plans || {})[cards[i].getAttribute('data-provider')] || [], answer.memoryGB, answer.cores);
      var text = cards[i].querySelector('[data-plan]');
      if (text) text.textContent = plan ? plan.name + ' · ' + plan.cpus + ' vCPU · ' + plan.memoryGB + ' GB' : 'No plan this big';
    }
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
    byId('run-current').textContent = root.querySelector('label[for="' + software.id + '"] .name').textContent;
    if (current) current.classList.remove('current');
    current = byId('size-' + friends.value + '-' + software.value);
    if (current) current.classList.add('current');
    showPlans(answer);
    if (announce) byId('answer-status').textContent = answer.announce;
    shown = '#friends=' + friends.value + '&run=' + software.value;
  }
  // choose shows and remembers the answer for what is checked, unless it is
  // already on show.
  function choose() {
    var friends = checked('friends'), software = checked('run');
    if (!friends || !software || '#friends=' + friends.value + '&run=' + software.value === shown) return;
    show(true);
    if (!compact) history.replaceState(null, '', shown);
  }

  function fromAddress() {
    if (compact) return;
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
    backdrop.classList.add('open');
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
    backdrop.classList.remove('open');
    if (!keep && before) before.checked = true;
    before = null;
    choose();
    if (returnFocus) runButton.focus();
  }

  root.addEventListener('change', function (e) {
    if (e.target.name !== 'friends' && e.target.name !== 'run') return;
    if (e.target.name === 'run' && sheetOpen()) return;
    choose();
  });
  if (!compact) {
    window.addEventListener('hashchange', function () {
      fromAddress();
      show(true);
    });
  }

  document.addEventListener('pointerdown', function () { arrowing = false; }, true);
  document.addEventListener('keydown', function (e) {
    arrowing = /^Arrow/.test(e.key);
    if (arrowing) setTimeout(function () { arrowing = false; });
    if (e.key === 'Escape') closeSheet(false, true);
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
    if (e.target.name === 'run' && !arrowing && sheetOpen()) closeSheet(true, true);
  });
  options.addEventListener('focusout', function (e) {
    if (e.relatedTarget && !options.contains(e.relatedTarget)) closeSheet(false, false);
  });
  sheetClose.addEventListener('click', function () { closeSheet(false, true); });
  backdrop.addEventListener('click', function () { closeSheet(false, true); });
  phone.addEventListener('change', function () { closeSheet(false, false); });

  fromAddress();
  show(false);
})();

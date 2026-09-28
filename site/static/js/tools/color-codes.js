// The colour codes tool (/tools/color-codes): the text maker, for Java or
// Bedrock, drawn as chat shows it and written as & or § codes, MiniMessage
// or a /tellraw command. The tables' codes copy through js/tools.js.
(function () {
  var t = window.pkTools;
  var mc = window.mcText;
  var root = document.querySelector('[data-cc-tool]');
  if (!t || !mc || !window.mcEditor || !root) return;
  var chat = root.querySelector('[data-chat]');
  var out = {
    amp: root.querySelector('#cc-out-amp'),
    sect: root.querySelector('#cc-out-sect'),
    mm: root.querySelector('#cc-out-mm'),
    json: root.querySelector('#cc-out-json'),
  };
  var hexNote = root.querySelector('[data-note-hex]');
  var bedrock = function () { return t.value(root, 'cc-edition') === 'bedrock'; };
  var editor = null;

  function update(chars) {
    var be = bedrock();
    mc.render(chat, chars, '#ffffff', true);
    out.amp.textContent = mc.legacy(chars, '&', t.value(root, 'cc-hex') || 'hash', false);
    out.sect.textContent = be ? mc.legacy(chars, '§', 'nearest', true) : mc.legacy(chars, '§', 'x', false);
    out.mm.textContent = mc.minimessage(chars, editor.gradients);
    out.json.textContent = '/tellraw @a ' + (be ? JSON.stringify({ rawtext: [{ text: mc.legacy(chars, '§', 'nearest', true) }] }) : mc.json(chars));
    hexNote.hidden = !(be && chars.some(function (c) { return c.st.c && !c.st.l; }));
  }

  editor = window.mcEditor(root.querySelector('[data-editor]'), { lines: 0, bedrock: bedrock, fallback: '#ffffff', onChange: update });

  // Java or Bedrock: Bedrock's own colours and its lack of underline,
  // strikethrough, & codes and MiniMessage, and its colours for the codes
  // both have.
  function edition() {
    var be = bedrock();
    root.classList.toggle('is-bedrock', be);
    editor.chars().forEach(function (c) {
      if (!c.st.l) return;
      var color = mc.colorOf(c.st.l, be);
      if (color) c.st.c = color;
      else c.st.l = null;
      if (be) { c.st.u = false; c.st.s = false; }
    });
    root.querySelectorAll('[data-java]').forEach(function (tab) { tab.hidden = be; });
    var shown = root.querySelector('.tool-tab[aria-selected="true"]');
    if (shown && shown.hidden) root.querySelector('#cc-tab-sect').click();
    editor.refresh();
  }
  t.radios(root, 'cc-edition').forEach(function (r) { r.addEventListener('change', edition); });
  t.radios(root, 'cc-hex').forEach(function (r) { r.addEventListener('change', function () { update(editor.chars()); }); });

  editor.set(mc.parseMiniMessage('<gray>Welcome to <gradient:#ffaa00:#ff5555><bold>Pip Land</bold></gradient><gray>, <aqua>have fun!', editor.gradients));
  if (bedrock()) edition();
})();

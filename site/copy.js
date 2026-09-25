// Shows the Copy button when the browser has a clipboard API.
(function () {
  var button = document.getElementById('copy');
  var status = document.getElementById('copy-status');
  if (!navigator.clipboard) return;
  button.hidden = false;
  button.addEventListener('click', function () {
    navigator.clipboard.writeText(document.getElementById('command').textContent).then(
      function () { status.textContent = 'Copied. Paste it into a terminal on your server.'; },
      function () { status.textContent = 'Copying failed; select the command and copy it instead.'; }
    );
  });
})();

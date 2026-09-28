// What the free tools on /tools share: copying a result with feedback,
// downloading a file made in the browser, counting tool_used for the
// analytics, colours typed as hex, and the radio groups the tools are built
// from. Each tool's own script (js/tools/…) uses it through window.pkTools.
// Nothing leaves the browser: the tools make their files on the page.
(function () {
  var site = window.playkeeperSite || { toast: function () {}, count: function () {} };
  var phone = window.matchMedia('(max-width: 639.98px)');

  // tool_used: a tool's result was taken, such as a download or a copy.
  function track(tool, action) { site.count('tool_used', { tool: tool, action: action }); }

  // copy puts text on the clipboard. The button says Copied for a moment, and
  // phones, where the button may be under a thumb, also get the toast.
  function copy(text, button, tool, action) {
    if (!navigator.clipboard || window.isSecureContext === false) {
      site.toast('Copying needs a secure page. Select the text and copy it instead.', 3000);
      return;
    }
    navigator.clipboard.writeText(text).then(function () {
      if (tool) track(tool, action || 'copy');
      if (button) {
        var label = button.querySelector('[data-copy-label]');
        var before = label ? label.getAttribute('data-copy-label') || label.textContent : '';
        if (label && !label.hasAttribute('data-copy-label')) label.setAttribute('data-copy-label', before);
        button.classList.add('is-copied');
        if (label) label.textContent = 'Copied';
        clearTimeout(button._copyTimer);
        button._copyTimer = setTimeout(function () {
          button.classList.remove('is-copied');
          if (label) label.textContent = before;
        }, 1600);
      }
      if (phone.matches) site.toast('Copied');
    }, function () {
      site.toast('Copying failed. Select the text and copy it instead.', 3000);
    });
  }

  // download saves a file made on the page.
  function download(blob, name, tool) {
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 30000);
    if (tool) track(tool, 'download');
  }

  // hex reads a colour typed as #rgb or #rrggbb, with or without the #, as
  // #rrggbb in lower case; anything else is null.
  function hex(s) {
    var m = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(String(s || '').trim());
    if (!m) return null;
    var h = m[1].toLowerCase();
    if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
    return '#' + h;
  }
  function rgb(h) {
    var n = parseInt(h.slice(1), 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  }
  function toHex(c) {
    return '#' + c.map(function (v) { return ('0' + Math.max(0, Math.min(255, Math.round(v))).toString(16)).slice(-2); }).join('');
  }
  // shade mixes a colour with black (t < 0) or white (t > 0).
  function shade(h, t) {
    var c = rgb(h);
    var to = t < 0 ? 0 : 255;
    var k = Math.abs(t);
    return toHex(c.map(function (v) { return v + (to - v) * k; }));
  }

  function radios(root, name) { return Array.prototype.slice.call(root.querySelectorAll('input[name="' + name + '"]')); }
  function value(root, name) {
    var on = radios(root, name).filter(function (r) { return r.checked; })[0];
    return on ? on.value : null;
  }
  function pick(root, name, v) {
    radios(root, name).forEach(function (r) { r.checked = r.value === v; });
  }

  // A colour group: swatches (radios) and a hex field beside them. Typing a
  // colour unticks the swatches; ticking a swatch fills in the field.
  // onChange gets the colour, or '' for a swatch that means none.
  function colorGroup(root, name, field, onChange) {
    var inputs = radios(root, name);
    function current() {
      var v = value(root, name);
      if (v !== null) return v;
      return hex(field && field.value) || '';
    }
    inputs.forEach(function (r) {
      r.addEventListener('change', function () {
        if (field) {
          field.value = r.value;
          field.removeAttribute('aria-invalid');
        }
        onChange(current());
      });
    });
    if (field) {
      field.addEventListener('input', function () {
        var h = hex(field.value);
        field.toggleAttribute('aria-invalid', !h && field.value.trim() !== '');
        if (!h) return;
        var match = inputs.filter(function (r) { return r.value === h; })[0];
        inputs.forEach(function (r) { r.checked = r === match; });
        onChange(h);
      });
      field.addEventListener('blur', function () {
        var h = hex(field.value);
        if (h) field.value = h;
        else { field.value = current(); field.removeAttribute('aria-invalid'); }
      });
    }
    return { get: current, set: function (v) { pick(root, name, v); if (field) field.value = v; } };
  }

  // Copy buttons with data-tool-copy copy the text of the element their
  // data-copy-from names, or their own data-copy-text.
  document.addEventListener('click', function (e) {
    var b = e.target.closest && e.target.closest('[data-tool-copy]');
    if (!b) return;
    var from = b.getAttribute('data-copy-from');
    var el = from ? document.querySelector(from) : null;
    var text = el ? ('value' in el && el.tagName !== 'BUTTON' ? el.value : el.textContent) : b.getAttribute('data-copy-text') || '';
    copy(text, b, b.getAttribute('data-tool-copy'), b.getAttribute('data-copy-action') || 'copy');
  });

  window.pkTools = {
    track: track,
    copy: copy,
    download: download,
    hex: hex,
    rgb: rgb,
    toHex: toHex,
    shade: shade,
    radios: radios,
    value: value,
    pick: pick,
    colorGroup: colorGroup,
    toast: site.toast,
    reduce: window.matchMedia('(prefers-reduced-motion: reduce)'),
    phone: phone,
  };
})();

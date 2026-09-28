// The JVM arguments tool (/tools/jvm-flags): Aikar's flags, ZGC or Java's
// defaults for a server, in start.sh, start.bat, user_jvm_args.txt or on
// their own, with a heap sized as Playkeeper sizes it; or the Minecraft
// Launcher's own arguments with the memory changed. internal/site/jvm.go has
// the same flags and numbers, and jvm_test.go checks they match.
(function () {
  var t = window.pkTools;
  var root = document.querySelector('[data-jvm-tool]');
  if (!t || !root) return;
  var TOOL = 'jvm-flags';

  var AIKAR = [
    '-XX:+UseG1GC',
    '-XX:+ParallelRefProcEnabled',
    '-XX:MaxGCPauseMillis=200',
    '-XX:+UnlockExperimentalVMOptions',
    '-XX:+DisableExplicitGC',
    '-XX:+AlwaysPreTouch',
    '-XX:G1NewSizePercent=30',
    '-XX:G1MaxNewSizePercent=40',
    '-XX:G1HeapRegionSize=8M',
    '-XX:G1ReservePercent=20',
    '-XX:G1HeapWastePercent=5',
    '-XX:G1MixedGCCountTarget=4',
    '-XX:InitiatingHeapOccupancyPercent=15',
    '-XX:G1MixedGCLiveThresholdPercent=90',
    '-XX:G1RSetUpdatingPauseTimePercent=5',
    '-XX:SurvivorRatio=32',
    '-XX:+PerfDisableSharedMem',
    '-XX:MaxTenuringThreshold=1',
    '-Dusing.aikars.flags=https://mcflags.emc.gs',
    '-Daikars.new.flags=true',
  ];
  // Aikar's values for a heap of LARGE_HEAP_MB or more.
  var AIKAR_LARGE = { '-XX:G1NewSizePercent': '40', '-XX:G1MaxNewSizePercent': '50', '-XX:G1HeapRegionSize': '16M', '-XX:G1ReservePercent': '15', '-XX:InitiatingHeapOccupancyPercent': '20' };
  var LARGE_HEAP_MB = 12288;
  var HEAP = { share: 4, minMB: 512, perModMB: 6, fabric: 768, neoforge: 1024 };
  var NEWEST_JAVA = 25;
  var LAUNCHER = '-XX:+UseCompactObjectHeaders -XX:+AlwaysPreTouch -XX:+UseStringDeduplication -XX:+UseZGC';
  var LAUNCHER_OLD = '-XX:+UnlockExperimentalVMOptions -XX:+UseG1GC -XX:G1NewSizePercent=20 -XX:G1ReservePercent=20 -XX:MaxGCPauseMillis=50 -XX:G1HeapRegionSize=32M';
  // Java 11 and newer won't start when the log's folder is missing, so the
  // scripts make it first.
  var GC_LOG = '-Xlog:gc*:logs/gc.log:time,uptime:filecount=5,filesize=1M';
  var GC_LOG_8 = ['-Xloggc:gc.log', '-verbose:gc', '-XX:+PrintGCDetails', '-XX:+PrintGCDateStamps', '-XX:+PrintGCTimeStamps', '-XX:+UseGCLogFileRotation', '-XX:NumberOfGCLogFiles=5', '-XX:GCLogFileSize=1M'];
  var JARS = { paper: 'paper.jar', vanilla: 'server.jar', fabric: 'fabric-server-launch.jar', neoforge: 'forge.jar' };

  var $ = function (s) { return root.querySelector(s); };
  var versionInput = $('#jvm-version');
  var javaOut = $('[data-java]');
  var memoryInput = $('#jvm-memory');
  var modsInput = $('#jvm-mods');
  var gameMemoryInput = $('#jvm-game-memory');
  var jarInput = $('#jvm-jar');
  var restart = $('#jvm-restart');
  var gclog = $('#jvm-gclog');
  var out = $('[data-out]');
  var outTitle = $('[data-out-title]');
  var next = $('[data-next]');
  var download = $('[data-download]');
  var flagsHint = $('[data-flags-hint]');
  var launcherHint = $('[data-launcher-hint]');
  var zgc = $('#jvm-flags-zgc');
  var java = NEWEST_JAVA;
  var jarTouched = false;

  // javaFor is the Java a Minecraft version runs on, as Playkeeper picks it
  // (minecraft.JavaFor), or 0 when it isn't a version: 8 up to 1.16.5, 16 for
  // 1.17, 17 up to 1.20.4, 21 up to 1.21.11 and NEWEST_JAVA from 26.1, and
  // for snapshots of those.
  function javaFor(v) {
    var m = /^(\d+)\.(\d+)(?:\.(\d+))?(?:[- ]\S*)?$/.exec(String(v).trim());
    if (!m) return 0;
    var major = Number(m[1]);
    var minor = Number(m[2]);
    var patch = Number(m[3] || 0);
    if (major !== 1) return NEWEST_JAVA;
    if (minor <= 16) return 8;
    if (minor === 17) return 16;
    if (minor < 20 || (minor === 20 && patch <= 4)) return 17;
    return 21;
  }

  // heapFor is the heap for a server with budgetMB of memory, as Playkeeper
  // sizes it (minecraft.HeapFor): a quarter, at least 512 MB, stays outside
  // the heap, and a mod loader keeps its own share plus a little for each mod
  // there, never more than half.
  function heapFor(budgetMB, type, mods) {
    var outside = Math.max(Math.floor(budgetMB / HEAP.share), HEAP.minMB);
    if (type === 'fabric' || type === 'neoforge') outside = Math.max(outside, Math.min(HEAP[type] + HEAP.perModMB * mods, Math.floor(budgetMB / 2)));
    return budgetMB - outside;
  }

  function gcFlags(kind, heapMB) {
    if (kind === 'aikar') {
      var large = heapMB >= LARGE_HEAP_MB;
      return AIKAR.map(function (f) {
        var name = f.split('=')[0];
        return large && AIKAR_LARGE[name] ? name + '=' + AIKAR_LARGE[name] : f;
      });
    }
    if (kind === 'zgc') {
      // Java 21 needs asking for generational ZGC; from 24 it's the only kind,
      // and 25 has compact object headers, as Mojang's launcher uses.
      return java >= 25 ? ['-XX:+UseZGC', '-XX:+UseCompactObjectHeaders', '-XX:+AlwaysPreTouch', '-XX:+UseStringDeduplication'] : ['-XX:+UseZGC', '-XX:+ZGenerational', '-XX:+AlwaysPreTouch', '-XX:+UseStringDeduplication'];
    }
    return [];
  }

  function gb(mb) {
    var v = Math.round((mb / 1024) * 10) / 10;
    return String(v) + ' GB';
  }

  // Quoting for a script: sh in single quotes, cmd in double quotes, only
  // when a word needs them.
  function sh(word) { return /^[A-Za-z0-9._\/=:+-]+$/.test(word) ? word : "'" + word.replace(/'/g, "'\\''") + "'"; }
  function cmd(word) { return /^[A-Za-z0-9._\\\/=:+-]+$/.test(word) ? word : '"' + word.replace(/%/g, '%%') + '"'; }

  function state() {
    var type = t.value(root, 'jvm-type');
    var budget = Math.round(Number(memoryInput.value) * 1024);
    var mods = Number(modsInput.value);
    var loader = type === 'fabric' || type === 'neoforge';
    var heap = heapFor(budget, type, loader ? mods : 0);
    return {
      mode: t.value(root, 'jvm-for'),
      type: type,
      budget: budget,
      heap: heap,
      loader: loader,
      mods: mods,
      flags: t.value(root, 'jvm-flags'),
      // NeoForge and Forge from 1.17 start from run.sh, which reads the
      // arguments from user_jvm_args.txt.
      argsFile: type === 'neoforge' && java >= 16,
      format: t.value(root, 'jvm-format'),
      jar: jarInput.value.trim() || JARS[type],
      restart: restart.checked,
      gclog: gclog.checked,
      gameMemory: Number(gameMemoryInput.value),
      launcher: t.value(root, 'jvm-launcher'),
    };
  }

  function args(s) {
    var list = ['-Xms' + s.heap + 'M', '-Xmx' + s.heap + 'M'].concat(gcFlags(s.flags, s.heap));
    if (s.gclog) list = list.concat(java >= 11 ? [GC_LOG] : GC_LOG_8);
    return list;
  }

  var FLAG_NAMES = { aikar: "Aikar's flags", zgc: 'ZGC', none: "Java's defaults" };
  function describe(s) { return 'A ' + gb(s.heap) + ' heap with ' + FLAG_NAMES[s.flags] + ', on Java ' + java + '.'; }

  function script(s) {
    var nogui = s.type === 'paper' ? '--nogui' : 'nogui';
    var mkLogs = s.gclog && java >= 11;
    if (s.format === 'bat') {
      var runBat = 'java ' + args(s).map(cmd).join(' ') + ' -jar ' + cmd(s.jar) + ' ' + nogui;
      var bat = ['@echo off', 'rem ' + describe(s) + ' playkeeper.io/tools/jvm-flags', 'cd /d "%~dp0"'];
      if (mkLogs) bat.push('if not exist logs mkdir logs');
      if (s.restart) bat.push(':start', runBat, 'echo Restarting in 5 seconds. Press Ctrl+C to stop.', 'timeout /t 5 /nobreak >nul', 'goto start');
      else bat.push(runBat, 'pause');
      return bat.join('\n');
    }
    var run = 'java ' + args(s).map(sh).join(' ') + ' -jar ' + sh(s.jar) + ' ' + nogui;
    var lines = ['#!/usr/bin/env sh', '# ' + describe(s) + ' playkeeper.io/tools/jvm-flags', 'cd "$(dirname "$0")"'];
    if (mkLogs) lines.push('mkdir -p logs');
    if (s.restart) lines.push('while true; do', '  ' + run, '  echo "Restarting in 5 seconds. Press Ctrl+C to stop."', '  sleep 5', 'done');
    else lines.push(run);
    return lines.join('\n');
  }

  function code(s) { return '<code>' + s + '</code>'; }

  // What's shown for a server: the formats its type has, the one picked, and
  // what to do with it.
  function server(s) {
    var formats = s.argsFile ? ['args', 'flags'] : ['sh', 'bat', 'flags'];
    t.radios(root, 'jvm-format').forEach(function (r) {
      var off = formats.indexOf(r.value) < 0;
      r.hidden = off;
      root.querySelector('label[for="' + r.id + '"]').hidden = off;
    });
    if (formats.indexOf(s.format) < 0) {
      s.format = formats[0];
      t.pick(root, 'jvm-format', s.format);
    }
    var logsNote = s.gclog && java >= 11 ? ' Make a ' + code('logs') + ' folder next to the server first.' : '';
    var text;
    if (s.format === 'flags') {
      text = args(s).join(' ');
      outTitle.textContent = 'Flags only';
      next.innerHTML = 'Paste them where your host’s panel takes JVM arguments.' + logsNote;
    } else if (s.format === 'args') {
      text = '# ' + describe(s) + ' playkeeper.io/tools/jvm-flags\n' + args(s).join('\n');
      outTitle.textContent = 'user_jvm_args.txt';
      next.innerHTML = 'Replace what’s in user_jvm_args.txt, then start with ' + code('./run.sh nogui') + ' or ' + code('run.bat nogui') + '.' + logsNote;
    } else {
      text = script(s);
      outTitle.textContent = s.format === 'bat' ? 'start.bat' : 'start.sh';
      next.innerHTML = s.format === 'bat'
        ? 'Save it next to ' + code(escapeHTML(s.jar)) + ' and double-click it.'
        : 'Save it next to ' + code(escapeHTML(s.jar)) + ', run ' + code('chmod +x start.sh') + ' once, then ' + code('./start.sh') + '.';
    }
    download.hidden = s.format === 'flags';
    return text;
  }

  function game(s) {
    var memory = s.gameMemory + 'G';
    var own = java >= 25 ? LAUNCHER : LAUNCHER_OLD;
    outTitle.textContent = 'JVM arguments';
    download.hidden = true;
    if (s.launcher === 'other') {
      launcherHint.textContent = 'Prism, the Modrinth App, CurseForge and others set the memory in their own setting.';
      next.innerHTML = 'Paste them into its JVM arguments, and set its memory to ' + s.gameMemory + ' GB.';
      return own;
    }
    launcherHint.textContent = 'Installations, then ⋯ › Edit › More options: JVM arguments.';
    next.innerHTML = 'Replace what’s under JVM arguments with it, then Save.';
    return (java >= 25 ? '-Xms' + memory + ' -Xmx' + memory + ' ' : '-Xmx' + memory + ' ') + own;
  }

  function escapeHTML(s) { return s.replace(/[&<>"']/g, function (c) { return '&#' + c.charCodeAt(0) + ';'; }); }

  // Each argument stays on one line where it fits, so a line breaks between
  // arguments rather than at the hyphen that starts one.
  function show(text) {
    out.innerHTML = text.split('\n').map(function (line) {
      return line.split(/( +)/).map(function (part) {
        return part === '' || part.trim() === '' ? part : '<span class="jvm-arg">' + escapeHTML(part) + '</span>';
      }).join('');
    }).join('\n');
  }

  function fill(input) {
    var min = Number(input.min);
    var pct = ((Number(input.value) - min) / (Number(input.max) - min)) * 100;
    input.style.setProperty('--fill', pct + '%');
  }

  var dockTitle = root.querySelector('[data-dock-title]');
  var dockInfo = root.querySelector('[data-dock-info]');

  function update() {
    var s = state();
    var isServer = s.mode === 'server';
    root.querySelector('[data-panel="server"]').hidden = !isServer;
    root.querySelector('[data-panel="game"]').hidden = isServer;
    $('[data-heap]').hidden = !isServer;
    $('[data-formats]').hidden = !isServer;
    $('[data-mods-field]').hidden = !s.loader;
    $('[data-jar-field]').hidden = s.argsFile;
    $('[data-restart-field]').hidden = s.argsFile;

    zgc.disabled = java < 21;
    if (zgc.disabled && s.flags === 'zgc') {
      t.pick(root, 'jvm-flags', 'aikar');
      s.flags = 'aikar';
    }
    var hint = {
      aikar: 'G1, tuned for Minecraft: what Paper recommends and Playkeeper uses.' + (s.heap >= LARGE_HEAP_MB ? ' Aikar’s larger values, for 12 GB of heap or more.' : ''),
      zgc: 'Pauses under a millisecond, for more memory and processor time.',
      none: java >= 17 ? 'Only the memory, which Paper suggests trying first on Java 17 or newer.' : 'Only the memory. On Java older than 17, Paper suggests Aikar’s flags.',
    }[s.flags];
    flagsHint.textContent = hint + (java < 21 ? ' ZGC needs Minecraft 1.20.5 or newer.' : '');

    $('[data-memory-value]').textContent = gb(s.budget);
    $('[data-mods-value]').textContent = String(s.mods);
    $('[data-game-memory-value]').textContent = s.gameMemory + ' GB';
    [memoryInput, modsInput, gameMemoryInput].forEach(fill);

    $('[data-heap-fill]').style.width = (s.heap / s.budget) * 100 + '%';
    $('[data-heap-size]').textContent = gb(s.heap);
    $('[data-outside-size]').textContent = gb(s.budget - s.heap);

    show(isServer ? server(s) : game(s));
    if (dockTitle) {
      dockTitle.firstChild.textContent = outTitle.textContent;
      dockInfo.textContent = isServer ? gb(s.heap) + ' heap · ' + FLAG_NAMES[s.flags] : s.gameMemory + ' GB for the game';
    }
  }

  var lastVersion = versionInput.value;
  function readVersion() {
    var j = javaFor(versionInput.value);
    versionInput.toggleAttribute('aria-invalid', !j);
    javaOut.classList.toggle('is-bad', !j);
    if (!j) {
      javaOut.textContent = 'Type a version like 1.21.1';
      return;
    }
    java = j;
    lastVersion = versionInput.value.trim();
    javaOut.innerHTML = 'Runs on <strong>Java ' + j + '</strong>';
    update();
  }

  versionInput.addEventListener('input', readVersion);
  versionInput.addEventListener('blur', function () {
    if (!javaFor(versionInput.value)) {
      versionInput.value = lastVersion;
      readVersion();
    }
  });
  t.radios(root, 'jvm-type').forEach(function (r) {
    r.addEventListener('change', function () {
      if (!jarTouched) jarInput.value = JARS[r.value];
      update();
    });
  });
  jarInput.addEventListener('input', function () { jarTouched = jarInput.value.trim() !== ''; update(); });
  ['jvm-for', 'jvm-flags', 'jvm-format', 'jvm-launcher'].forEach(function (name) {
    t.radios(root, name).forEach(function (r) { r.addEventListener('change', update); });
  });
  [memoryInput, modsInput, gameMemoryInput].forEach(function (input) { input.addEventListener('input', update); });
  [restart, gclog].forEach(function (input) { input.addEventListener('change', update); });

  download.addEventListener('click', function () {
    var s = state();
    var name = outTitle.textContent;
    // cmd.exe reads a .bat with Windows line ends.
    var text = s.format === 'bat' ? out.textContent.replace(/\n/g, '\r\n') + '\r\n' : out.textContent + '\n';
    t.download(new Blob([text], { type: 'text/plain' }), name, TOOL);
  });

  // On a phone, Copy stays at hand in a bar at the bottom while the settings
  // scroll and the arguments are out of view.
  var dock = root.querySelector('[data-dock]');
  var preview = $('[data-preview]');
  var controls = $('[data-controls]');
  if (dock && 'IntersectionObserver' in window) {
    var seen = { preview: true, controls: false };
    var watch = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) { seen[en.target === preview ? 'preview' : 'controls'] = en.isIntersecting; });
      var shown = !seen.preview && seen.controls;
      dock.classList.toggle('is-shown', shown);
      dock.toggleAttribute('inert', !shown);
    });
    watch.observe(preview);
    watch.observe(controls);
  }

  readVersion();
})();

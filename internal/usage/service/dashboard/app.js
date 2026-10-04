// Playkeeper stats: the counts GET /v1/summary answers, drawn. The read token
// stays in this browser (localStorage, or sessionStorage when it isn't to be
// remembered) and goes only to this service, in the Authorization header.
// Everything is written as text, never as HTML.

const KEY = 'playkeeper-stats-token'
const REFRESH_EVERY = 5 * 60 * 1000
const ABOUT = 'https://github.com/CIYAhq/playkeeper#usage-stats'

const $ = (id) => document.getElementById(id)
const nf = new Intl.NumberFormat('en-GB')
const shortDay = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' })
const longDay = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' })
const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })

const STEPS = {
  docker: 'Installing Docker', users: 'Creating users', directories: 'Creating directories', binary: 'Installing Playkeeper',
  'sudo-link': 'Linking the command', config: 'Writing settings', certificate: 'HTTPS certificate', services: 'Starting services',
  firewall: 'Opening the firewall', manifest: 'Install manifest', other: 'Elsewhere',
}
const CHECKS = {
  root: 'Not run as root', arch: 'CPU type', systemd: 'No systemd', memory: 'Memory', disk: 'Disk space', port: 'Port in use',
  existing: 'Existing Minecraft setup', installed: 'Already installed', docker: 'Docker', os: 'System',
}
const SOURCES = {
  'playkeeper.io': 'playkeeper.io command', github: 'GitHub get.sh', mirror: 'Another location', tarball: 'Release tarball',
  source: 'Built from source', unknown: 'Before 0.4.4',
}
const SOURCE_ORDER = Object.keys(SOURCES)
const SYSTEMS = {
  ubuntu: 'Ubuntu', debian: 'Debian', almalinux: 'AlmaLinux', rocky: 'Rocky Linux', ol: 'Oracle Linux', amzn: 'Amazon Linux',
  rhel: 'RHEL', centos: 'CentOS Stream', fedora: 'Fedora',
}
const ARCH = { amd64: 'x86 (amd64)', arm64: 'ARM (arm64)' }
const ADDRESS = { free: 'Free playkeeper.me name', own: 'Own domain', ip: 'IP address only' }
const KIND = { dashboard: 'Dashboard', joined: 'Joined to another dashboard' }
const REACHED = {
  none: 'No account yet, or before 0.4.18', account: 'Made an account', server: 'A server came online', played: 'Someone played',
  friends: 'Friends played',
}
const REACHED_ORDER = Object.keys(REACHED)
const SERVERS = { 0: 'No servers', 1: '1 server', 2: '2 servers', '3-5': '3 to 5', '6-10': '6 to 10', '11+': '11 or more' }
const OUTCOMES = { succeeded: 'Succeeded', failed: 'Failed', refused: 'Refused', pending: 'No result that day' }
const WINDOWS = { '1d': 'day', '7d': '7 days', '30d': '30 days' }
// The funnel's steps: the site's own (visitors and demo opens from its
// analytics, copies of the install command it counts here), then installs
// made with the playkeeper.io command.
const FUNNEL = [
  ['visitors', 'Visitors', 'to playkeeper.io'],
  ['demoOpens', 'Opened the demo', ''],
  ['commandCopies', 'Copied the install command', ''],
  ['started', 'Started an install', 'with the playkeeper.io command'],
  ['succeeded', 'Install succeeded', ''],
  ['stillRunning', 'Still running', 'a heartbeat in the last day'],
]
const percent = new Intl.NumberFormat('en-GB', { style: 'percent', maximumFractionDigits: 1 })

// signins counts sign-ins and sign-outs, so counts that arrive after one are
// dropped rather than shown.
const state = { summary: null, split: 'outcome', window: '30d', funnel: '7d', day: -1, last: 0, busy: false, signins: 0 }

// The token, where it's kept. memory holds it for this page alone when the
// browser refuses storage.
let memory = ''
function stored () {
  try {
    return localStorage.getItem(KEY) || sessionStorage.getItem(KEY) || memory
  } catch {
    return memory
  }
}
function keep (token, remember) {
  forget()
  try {
    (remember ? localStorage : sessionStorage).setItem(KEY, token)
  } catch {
    memory = token
  }
}
function forget () {
  memory = ''
  try {
    localStorage.removeItem(KEY)
    sessionStorage.removeItem(KEY)
  } catch { /* nothing kept */ }
}

async function fetchSummary (token) {
  let res
  try {
    res = await fetch('/v1/summary', { headers: { Authorization: 'Bearer ' + token }, cache: 'no-store', credentials: 'omit', redirect: 'error' })
  } catch {
    return { problem: 'Couldn’t reach the stats service. Check the connection, then refresh.' }
  }
  if (res.ok) {
    try {
      return { summary: await res.json() }
    } catch {
      return { problem: 'The service’s answer couldn’t be read. Refresh to try again.' }
    }
  }
  let code = ''
  try {
    code = (await res.json()).code || ''
  } catch { /* not JSON */ }
  switch (true) {
    case res.status === 401:
      return { status: 401, problem: 'That token didn’t open the counts. Paste STATS_READ_TOKEN as it’s set in Coolify.' }
    case res.status === 403 && code === 'not_configured':
      return { status: 403, problem: 'The service has no read token yet: set STATS_READ_TOKEN in Coolify and redeploy.' }
    case res.status === 429: {
      const minutes = Math.max(1, Math.ceil((Number(res.headers.get('Retry-After')) || 60) / 60))
      return { status: 429, problem: `Too many requests from this address. Try again in ${minutes} min.` }
    }
  }
  return { status: res.status, problem: `The service answered HTTP ${res.status}. Try again in a moment.` }
}

function el (tag, cls, ...children) {
  const e = document.createElement(tag)
  if (cls) e.className = cls
  e.append(...children)
  return e
}
const count = (n) => nf.format(n || 0)
const plural = (n, word) => `${count(n)} ${word}${n === 1 ? '' : 's'}`
const date = (day) => new Date(day + 'T00:00:00Z')
const attempts = (o) => (o ? o.started + o.refused : 0)
const sourceName = (k) => SOURCES[k] || k
const sourceClass = (k) => (SOURCE_ORDER.includes(k) ? 'src-' + k.replace(/[^a-z0-9]+/g, '-') : 'src-other')
const systemName = (k) => {
  const [id, ...release] = k.split(' ')
  return [SYSTEMS[id] || id, ...release].join(' ')
}

// sorted is a count map's entries, most first, or in order when one is given.
function sorted (m, order) {
  const list = Object.entries(m || {}).filter(([, n]) => n > 0)
  if (order) return list.sort((a, b) => rank(order, a[0]) - rank(order, b[0]) || b[1] - a[1])
  return list.sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
}
const rank = (order, k) => (order.includes(k) ? order.indexOf(k) : order.length)

function view (name) {
  $('signin').hidden = name !== 'signin'
  $('dashboard').hidden = name !== 'dashboard'
  $('actions').hidden = name === 'signin'
}

function status (text) {
  $('status').textContent = text || ''
  $('status').hidden = !text
}

function showSignIn (problem) {
  view('signin')
  status('')
  $('signin-error').textContent = problem || ''
  $('signin-error').hidden = !problem
  if (problem) $('token').focus()
}

async function refresh () {
  const token = stored()
  if (!token) return showSignIn()
  if (state.busy) return
  const signin = state.signins
  state.busy = true
  $('refresh').disabled = true
  if (!state.summary) {
    view('loading')
    status('Reading the counts…')
  }
  const r = await fetchSummary(token)
  state.busy = false
  $('refresh').disabled = false
  if (signin !== state.signins) return
  if (r.summary) return show(r.summary)
  if (r.status === 401) {
    forget()
    state.summary = null
    return showSignIn(r.problem)
  }
  if (!state.summary) view('problem')
  status(r.problem)
}

function show (summary) {
  state.summary = summary
  state.last = Date.now()
  if (state.day < 0 || state.day >= summary.daily.length) state.day = summary.daily.length - 1
  status('')
  view('dashboard')
  $('updated').textContent = 'Updated ' + clock.format(new Date(summary.generatedAt))
  renderActive(summary)
  renderFunnel(summary)
  renderRunning(summary)
  renderInstalls(summary)
  renderFailures(summary)
  renderMachines(summary)
  renderFoot(summary)
}

function renderActive (s) {
  for (const w of Object.keys(WINDOWS)) {
    const a = s.active[w]
    $('active-' + w).textContent = count(a.installs)
    $('active-' + w + '-sub').textContent = `${plural(a.servers, 'server')} · ${count(a.running)} running`
  }
}

// share is n as a part of of: "12.5%", or empty when there's no part to
// give.
function share (n, of) {
  if (n == null || !of) return ''
  const p = n / of
  return p > 0 && p < 0.001 ? '<0.1%' : percent.format(p)
}

function renderFunnel (s) {
  for (const b of document.querySelectorAll('[data-funnel]')) b.setAttribute('aria-pressed', String(b.dataset.funnel === state.funnel))
  const f = (s.funnel || {})[state.funnel] || {}
  const values = FUNNEL.map(([k]) => (f[k] == null ? null : f[k]))
  const max = Math.max(1, ...values.filter((v) => v != null))
  $('funnel').replaceChildren(...FUNNEL.map(([, name, hint], i) => {
    const v = values[i]
    const head = el('div', 'funnel-head', el('span', 'funnel-name', name, hint ? el('span', 'funnel-hint', ' ' + hint) : ''))
    const part = i > 0 ? share(v, values[i - 1]) : ''
    head.append(el('span', 'funnel-share', part ? `${part} of the step before` : ''), el('span', 'num', v == null ? '—' : count(v)))
    const fill = el('span', 'fill')
    fill.style.width = ((v || 0) / max) * 100 + '%'
    return el('li', v == null ? 'unknown-step' : '', head, el('span', 'track', fill))
  }))
  const whole = share(values[5], values[0])
  $('funnel-sub').textContent = whole
    ? `${whole} of playkeeper.io's visitors in the last ${WINDOWS[state.funnel]} have Playkeeper running`
    : `playkeeper.io's visitors in the last ${WINDOWS[state.funnel]}, then installs made with its command`
  const site = s.site || {}
  let note
  if (!site.configured) note = 'Visitors and demo opens need a read key for the site’s analytics: STATS_OA_KEY in Coolify (services/stats/README.md).'
  else if (site.error && site.readAt) note = `The site’s analytics couldn’t be read just now (${site.error}); visitors and demo opens are from ${clock.format(new Date(site.readAt))}.`
  else if (site.error) note = `The site’s analytics couldn’t be read (${site.error}).`
  else if (site.readAt) note = `Visitors and demo opens from the site’s analytics, read at ${clock.format(new Date(site.readAt))}.`
  else note = ''
  $('funnel-note').textContent = `${note} Each step counts the same window, not the same people, so a step can be larger than the one before it.`.trim()
}

// chart draws a column for each day, a stack of segments scaled to the
// highest day, or none when every day is empty. pick, when given, makes each
// column a button choosing its day.
function chart (box, axis, days, stacks, none, describe, pick) {
  const totals = stacks.map((stack) => stack.reduce((t, x) => t + x.n, 0))
  const max = Math.max(0, ...totals)
  const items = [max ? el('span', 'max', count(max)) : el('span', 'empty', none)]
  days.forEach((d, i) => {
    const col = el(pick ? 'button' : 'span', 'col')
    if (pick) {
      col.type = 'button'
      col.setAttribute('aria-pressed', String(i === state.day))
      col.addEventListener('click', () => pick(i))
    } else {
      col.setAttribute('role', 'img')
    }
    col.setAttribute('aria-label', describe(d))
    col.title = describe(d)
    for (const x of stacks[i]) {
      if (!x.n) continue
      const seg = el('span', 'seg ' + x.cls)
      seg.style.height = (x.n / max) * 100 + '%'
      col.append(seg)
    }
    items.push(col)
  })
  box.replaceChildren(...items)
  const ticks = days.length ? [0, Math.floor(days.length / 2), days.length - 1] : []
  axis.replaceChildren(...ticks.map((i) => el('span', '', shortDay.format(date(days[i].day)))))
}

function legend (ul, parts) {
  ul.replaceChildren(...parts.map((p) => el('li', '', el('span', 'swatch ' + p.cls), el('span', '', p.name, ' ', el('span', 'num', count(p.n))))))
}

function renderRunning (s) {
  chart($('running-chart'), $('running-axis'), s.daily, s.daily.map((d) => [{ cls: 'running', n: d.active }]),
    'No heartbeats yet: machines send them from Playkeeper 0.4.4 on.',
    (d) => `${longDay.format(date(d.day))}: ${plural(d.active, 'machine')} running`)
}

function outcomeStack (d) {
  return [
    { cls: 'succeeded', n: d.succeeded },
    { cls: 'failed', n: d.failed },
    { cls: 'refused', n: d.refused },
    { cls: 'pending', n: Math.max(0, d.started - d.succeeded - d.failed) },
  ]
}

function daySources (days) {
  const keys = new Set()
  for (const d of days) for (const [k, o] of Object.entries(d.bySource || {})) if (attempts(o) > 0) keys.add(k)
  return [...keys].sort((a, b) => rank(SOURCE_ORDER, a) - rank(SOURCE_ORDER, b) || a.localeCompare(b))
}

const dayText = (d) => `${longDay.format(date(d.day))}: ${count(d.started)} started, ${count(d.succeeded)} succeeded, ${count(d.failed)} failed, ${count(d.refused)} refused`

function renderInstalls (s) {
  const m = s.installs['30d']
  let totals = `Last 30 days: ${count(m.started)} started · ${count(m.succeeded)} succeeded · ${count(m.failed)} failed · ${count(m.refused)} refused`
  if (m.unfinished) totals += ` · ${count(m.unfinished)} unfinished`
  $('installs-totals').textContent = totals
  for (const b of document.querySelectorAll('[data-split]')) b.setAttribute('aria-pressed', String(b.dataset.split === state.split))
  const days = s.daily
  let stacks, parts
  if (state.split === 'outcome') {
    stacks = days.map(outcomeStack)
    parts = Object.entries(OUTCOMES).map(([cls, name]) => ({ cls, name, n: stacks.reduce((t, st) => t + st.find((x) => x.cls === cls).n, 0) }))
  } else {
    const keys = daySources(days)
    stacks = days.map((d) => keys.map((k) => ({ cls: sourceClass(k), n: attempts((d.bySource || {})[k]) })))
    parts = keys.map((k) => ({ cls: sourceClass(k), name: sourceName(k), n: days.reduce((t, d) => t + attempts((d.bySource || {})[k]), 0) }))
  }
  chart($('installs-chart'), $('installs-axis'), days, stacks, 'No installs yet: the installer reports from Playkeeper 0.4.4 on.', dayText, (i) => {
    state.day = i
    renderInstalls(s)
  })
  legend($('installs-legend'), parts)
  const d = days[state.day]
  const box = $('installs-day')
  box.replaceChildren(el('strong', '', longDay.format(date(d.day))),
    ` · ${count(d.started)} started · ${count(d.succeeded)} succeeded · ${count(d.failed)} failed · ${count(d.refused)} refused`)
  const bySource = daySources([d]).map((k) => `${sourceName(k)} ${count(attempts(d.bySource[k]))}`)
  if (bySource.length) box.append(el('br'), bySource.join(' · '))
}

// bars lists entries as labelled bars, each as wide as its share of them.
function bars (ul, entries, name, opts = {}) {
  const total = entries.reduce((t, [, n]) => t + n, 0)
  if (!total) {
    ul.replaceChildren(el('li', '', el('span', 'none', opts.none || 'None yet')))
    return
  }
  ul.replaceChildren(...entries.map(([k, n]) => {
    const fill = el('span', 'fill' + (opts.cls ? ' ' + opts.cls : ''))
    fill.style.width = (n / total) * 100 + '%'
    const li = el('li', '', el('span', 'name', name(k)), el('span', 'num', count(n)), el('span', 'track', fill))
    li.title = `${name(k)}: ${count(n)}`
    return li
  }))
}

function renderFailures (s) {
  const m = s.installs['30d']
  bars($('failed-steps'), sorted(m.failedSteps), (k) => STEPS[k] || k, { cls: 'failed', none: 'No failed installs in the last 30 days.' })
  bars($('refused-checks'), sorted(m.refusedChecks), (k) => CHECKS[k] || k, { cls: 'refused', none: 'No refused installs in the last 30 days.' })
}

function renderMachines (s) {
  const a = s.active[state.window]
  for (const b of document.querySelectorAll('[data-window]')) b.setAttribute('aria-pressed', String(b.dataset.window === state.window))
  $('machines-sub').textContent = `${plural(a.installs, 'machine')} ran in the last ${WINDOWS[state.window]}, with ${plural(a.servers, 'Minecraft server')}, ${count(a.running)} running`
  const parts = [
    { cls: 'on', name: 'On our domain (playkeeper.io command)', n: a.onOurDomain },
    { cls: 'off', name: 'Off our domain (GitHub, mirrors, tarballs, builds)', n: a.offOurDomain },
    { cls: 'unknown', name: 'Installed before 0.4.4', n: a.unknownSource },
  ]
  const total = parts.reduce((t, p) => t + p.n, 0)
  const split = $('domain-split')
  split.replaceChildren(...parts.filter((p) => p.n > 0).map((p) => {
    const seg = el('span', p.cls)
    seg.style.width = (p.n / total) * 100 + '%'
    return seg
  }))
  split.setAttribute('role', 'img')
  split.setAttribute('aria-label', parts.map((p) => `${p.name}: ${count(p.n)}`).join(', '))
  legend($('domain-legend'), parts)
  bars($('by-version'), sorted(a.byVersion), (k) => k)
  bars($('by-os'), sorted(a.byOS), systemName)
  bars($('by-arch'), sorted(a.byArch), (k) => ARCH[k] || k)
  bars($('by-address'), sorted(a.byAddress), (k) => ADDRESS[k] || k)
  bars($('servers-per'), Object.keys(SERVERS).map((k) => [k, (a.serversPerInstall || {})[k] || 0]), (k) => SERVERS[k])
  bars($('by-source'), sorted(a.bySource, SOURCE_ORDER), sourceName)
  bars($('by-kind'), sorted(a.byKind), (k) => KIND[k] || k)
  bars($('by-reached'), sorted(a.byReached, REACHED_ORDER), (k) => REACHED[k] || k)
  const channels = sorted(a.byChannel)
  $('channels').hidden = channels.length === 0
  bars($('by-channel'), channels, (k) => k)
}

function renderFoot (s) {
  const link = el('a', '', 'What installs send')
  link.href = ABOUT
  link.target = '_blank'
  link.rel = 'noopener noreferrer'
  $('foot').replaceChildren(
    `Test installs left out of every count: ${count(s.test.started30d)} started in 30 days, ${count(s.test.active7d)} running in 7 days. Counts only, never an install ID or address. `,
    link)
}

$('signin-form').addEventListener('submit', async (e) => {
  e.preventDefault()
  const input = $('token')
  const token = input.value.trim()
  if (!token) return showSignIn('Paste the read token first.')
  $('signin-button').disabled = true
  const r = await fetchSummary(token)
  $('signin-button').disabled = false
  if (!r.summary) return showSignIn(r.problem)
  state.signins++
  keep(token, $('remember').checked)
  input.value = ''
  show(r.summary)
})
$('refresh').addEventListener('click', () => refresh())
$('signout').addEventListener('click', () => {
  state.signins++
  forget()
  state.summary = null
  $('token').value = ''
  showSignIn()
})
for (const b of document.querySelectorAll('[data-split]')) {
  b.addEventListener('click', () => {
    state.split = b.dataset.split
    renderInstalls(state.summary)
  })
}
for (const b of document.querySelectorAll('[data-funnel]')) {
  b.addEventListener('click', () => {
    state.funnel = b.dataset.funnel
    renderFunnel(state.summary)
  })
}
for (const b of document.querySelectorAll('[data-window]')) {
  b.addEventListener('click', () => {
    state.window = b.dataset.window
    renderMachines(state.summary)
  })
}
setInterval(() => {
  if (document.visibilityState === 'visible' && state.summary) refresh()
}, REFRESH_EVERY)
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && state.summary && Date.now() - state.last > REFRESH_EVERY) refresh()
})
refresh()

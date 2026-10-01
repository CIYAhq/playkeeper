import { html, type Html } from './html.ts'
import type { Allowance, Offer, Shelf, Storefront } from './store.ts'

/** The store's name until the business has one on Whop. */
const fallbackName = 'Minecraft server hosting'
const source = 'https://github.com/CIYAhq/playkeeper/tree/main/services/whop-store'
const guide = 'https://playkeeper.io/guides/start-a-minecraft-hosting-company'

const nameOf = (s?: Storefront) => s?.name || fallbackName

interface Frame {
  title: string
  description: string
  store?: Storefront
  /** A page for the store's owner only, kept out of search engines. */
  noindex?: boolean
}

function layout(frame: Frame, body: Html) {
  const s = frame.store
  const name = nameOf(s)
  const mark = s?.logo
    ? html`<img src="${s.logo}" alt="" width="32" height="32">`
    : html`<span class="mark" aria-hidden="true">${[...name][0]?.toUpperCase() ?? ''}</span>`
  return html`<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${frame.title}</title>
<meta name="description" content="${frame.description}">
${frame.noindex ? html`<meta name="robots" content="noindex">` : ''}
<link rel="icon" href="${s?.logo || '/favicon.svg'}">
<link rel="stylesheet" href="/store.css">
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
<header class="top">
<div class="wrap top-row">
<a class="brand" href="/">${mark}<span>${name}</span></a>
<nav aria-label="Store">
<a href="/#plans">Plans</a>
<a href="/#questions">Questions</a>
${s?.dashboard ? html`<a class="btn btn-outline btn-sm" href="${s.dashboard}">Sign in</a>` : ''}
</nav>
</div>
</header>
<main id="main" tabindex="-1">
${body}
</main>
<footer class="foot">
<div class="wrap">
<p class="foot-links"><span>${name}</span><a href="${s?.terms || '/terms'}">Terms</a><a href="https://playkeeper.io">Runs on Playkeeper</a><a href="${source}">Source code (AGPL-3.0)</a></p>
<p class="fine">Not an official Minecraft service. Not approved by or associated with Mojang or Microsoft.</p>
</div>
</footer>
</body>
</html>`
}

const gb = (n: number) => (Number.isInteger(n) ? String(n) : n.toFixed(1))

function facts(a: Allowance) {
  const servers = a.servers === 1 ? '1 server' : `Up to ${a.servers} servers`
  const memory = `${gb(a.memoryGB)} GB of memory${a.servers > 1 ? ' between them' : ''}`
  return html`<ul class="facts"><li>${servers}</li><li>${memory}</li></ul>`
}

function button(o: Offer) {
  const kind = o.action.kind
  switch (kind) {
    case 'buy':
      return html`<a class="btn btn-primary" href="${o.action.url}">Choose ${o.name}</a>`
    case 'waitlist':
      return html`<a class="btn btn-outline" href="${o.action.url}">Join the waitlist<span class="visually-hidden"> for ${o.name}</span></a>`
    case 'sold-out':
      return html`<p class="sold-out">Sold out</p>`
    default: {
      const never: never = kind
      throw new Error(`unknown action ${String(never)}`)
    }
  }
}

function card(o: Offer, heading: 'h3' | 'h4') {
  const id = `offer-${o.id}`
  const title = heading === 'h3' ? html`<h3 id="${id}">${o.name}</h3>` : html`<h4 id="${id}">${o.name}</h4>`
  return html`<article class="plan" aria-labelledby="${id}">
${title}
<p class="price">${o.price}</p>
${o.trialDays > 0 ? html`<p class="trial">${o.trialDays}-day free trial</p>` : ''}
${o.allowance ? facts(o.allowance) : ''}
${o.description ? html`<p class="desc">${o.description}</p>` : ''}
${button(o)}
</article>`
}

function shelf(sh: Shelf, many: boolean) {
  const head = many
    ? html`<h3 class="shelf-title">${sh.title}</h3>${sh.headline ? html`<p class="shelf-sub">${sh.headline}</p>` : ''}`
    : sh.headline
      ? html`<p class="shelf-sub">${sh.headline}</p>`
      : ''
  return html`<div class="shelf">
${head}
<ul class="plans">
${sh.offers.map((o) => html`<li>${card(o, many ? 'h4' : 'h3')}</li>`)}
</ul>
</div>`
}

/** A store whose products each sell one plan, as a hosted copy's do, shows its plans as one row, cheapest first. */
function rows(shelves: Shelf[]): Shelf[] {
  if (shelves.length < 2 || shelves.some((sh) => sh.offers.length !== 1)) return shelves
  return [{ id: 'plans', title: '', headline: '', offers: shelves.flatMap((sh) => sh.offers) }]
}

function open(s: Storefront) {
  const shown = rows(s.shelves)
  return html`${shown.map((sh) => shelf(sh, shown.length > 1))}
<p class="fine">You pay on Whop and can cancel there any time. Your servers keep running until the time you’ve paid for ends.</p>`
}

/** What a store that isn't taking orders shows at the top: when it opens, and the way in for its owner. */
function closed(s: Storefront) {
  return html`<div id="plans" class="closed">
<h2>Not taking orders yet</h2>
<p>${nameOf(s)} opens here soon.</p>
<p class="yours"><span>Is this your store?</span> <a class="btn btn-primary" href="/setup">Set up my store</a></p>
</div>`
}

interface Step {
  title: string
  text: string
  button: Html
}

const dashboardOf = (s: Storefront) => (s.business ? `https://whop.com/dashboard/${s.business}` : 'https://whop.com/dashboard')

function setupSteps(s: Storefront): Step[] {
  const go = (label: string, href: string) => html`<a class="btn btn-primary" href="${href}">${label}</a>`
  if (s.cloudApp) {
    return [
      { title: 'Connect Playkeeper Cloud', text: `Pick ${s.name || 'this business'} and approve it.`, button: go('Connect Playkeeper Cloud', `https://whop.com/apps/${s.cloudApp}/install`) },
      { title: 'Open Playkeeper Cloud', text: 'Find it under Apps in your Whop dashboard.', button: go('Open my dashboard', dashboardOf(s)) },
      { title: 'Open your store', text: 'In Playkeeper Cloud, press Open the store.', button: go('See my store', '/') },
    ]
  }
  return [
    { title: 'Install Playkeeper', text: 'Put it on your own server.', button: go('Get Playkeeper', 'https://playkeeper.io') },
    { title: 'Connect this store', text: 'Paste a Whop API key in Playkeeper’s Settings › Sell on Whop.', button: go('Get an API key', 'https://whop.com/dashboard/developer') },
    { title: 'Show your plans', text: 'Make your plans visible on Whop.', button: go('Open my dashboard', dashboardOf(s)) },
  ]
}

function questions(name: string): [string, string][] {
  return [
    ['What do I get?', `A Minecraft: Java Edition server from ${name}, within your plan’s servers and memory. You run it from your own dashboard: starting and stopping it, the console, its files and backups, plugins, mods and modpacks, and who can join.`],
    ['How soon can I play?', `A few minutes after checkout, ${name} tells you in your Whop messages that your server is ready. Sign in with Whop and start it: the first start takes a minute or two.`],
    ['Which versions can I run?', 'Minecraft: Java Edition 1.20.1 and newer, as Paper, Purpur, Vanilla, Fabric, Quilt, NeoForge or Forge, and modpacks from Modrinth and CurseForge.'],
    ['Can my friends join?', 'Yes. Your server has an address to share, and you choose who can join on its allowlist.'],
    ['What happens if I cancel?', 'Your servers keep running until the time you’ve paid for ends. Then they stop, and you can still sign in to download their backups. Renew within 14 days and everything is back as it was. After 14 days your servers are deleted, so download your worlds first.'],
    ['Can I change my plan?', 'Yes, on Whop. Your dashboard follows the new plan’s servers and memory.'],
    ['How do payments and refunds work?', `Whop takes the payments and sends the receipts. To ask for a refund, message ${name} on Whop.`],
    ['Is this an official Minecraft service?', `No. ${name} rents out server time. You play with your own Minecraft: Java Edition account, and accept Mojang’s EULA when you create a server. Not approved by or associated with Mojang or Microsoft.`],
  ]
}

export function homePage(s: Storefront) {
  const name = nameOf(s)
  const lead = s.description || 'Choose a plan, sign in with Whop, and run your server from your own dashboard, with plugins, mods, modpacks and backups.'
  const selling = s.shelves.length > 0
  return layout(
    { title: `${name}: Minecraft server hosting`, description: lead, store: s },
    html`<section class="hero">
<div class="wrap">
<p class="eyebrow">Minecraft: Java Edition server hosting</p>
<h1>Your own Minecraft server, and the dashboard to run it</h1>
<p class="lead">${lead}</p>
${selling ? html`<p><a class="btn btn-primary btn-lg" href="#plans">See the plans</a></p>` : closed(s)}
</div>
</section>
${
  selling
    ? html`<section id="plans" class="band" aria-labelledby="plans-title">
<div class="wrap">
<h2 id="plans-title">Plans</h2>
${open(s)}
</div>
</section>`
    : ''
}
<section class="band band-white" aria-labelledby="how-title">
<div class="wrap">
<h2 id="how-title">How it works</h2>
<ol class="steps">
<li><h3>Choose a plan</h3><p>Pay on Whop’s checkout. Your plan renews until you cancel it on Whop.</p></li>
<li><h3>Sign in with Whop</h3><p>${name} tells you in your Whop messages when your server is ready. Open the dashboard and sign in with your Whop account: there’s no password to make.</p></li>
<li><h3>Start your server</h3><p>It’s waiting for its first start. Start it, share its address with your friends, and add plugins, mods or a modpack whenever you like.</p></li>
</ol>
</div>
</section>
<section id="questions" class="band" aria-labelledby="questions-title">
<div class="wrap narrow">
<h2 id="questions-title">Questions</h2>
${questions(name).map(([q, a]) => html`<details><summary>${q}</summary><p>${a}</p></details>`)}
</div>
</section>`,
  )
}

export function termsPage(s: Storefront) {
  const name = nameOf(s)
  return layout(
    { title: `Terms of service: ${name}`, description: `The terms for ${name}’s Minecraft server hosting.`, store: s },
    html`<section class="band">
<div class="wrap narrow prose">
<h1>Terms of service</h1>
<p class="lead">The terms for ${name}’s Minecraft server hosting. Buying a plan means you agree to them, and to Whop’s terms for the payment.</p>
<h2>What you get</h2>
<p>A plan gives you servers from ${name}, up to the plan’s number of servers and memory, which you run from your own dashboard while the plan lasts.</p>
<h2>Minecraft</h2>
<p>You need your own Minecraft: Java Edition account to play, and you accept Mojang’s End User License Agreement when you create a server. If you charge your players, you follow Minecraft’s usage guidelines. This isn’t an official Minecraft service, and isn’t approved by or associated with Mojang or Microsoft.</p>
<h2>Using your servers</h2>
<p>Keep to the law and to these rules. Don’t use your servers or the machines they run on to attack, scan or flood other computers, to relay other people’s traffic, to mine cryptocurrency, to send spam or spread malware, to share what infringes someone else’s rights, to reach other customers’ servers or get around your plan’s limits, or to resell your servers. ${name} may stop a server that breaks these rules and end its plan.</p>
<h2>Payments</h2>
<p>Whop takes the payments. A plan renews until you cancel it on Whop, and a cancellation takes effect when the time you’ve paid for ends. Refunds are up to ${name}, within what Whop’s terms allow.</p>
<h2>When a plan ends</h2>
<p>Your servers stop, and you can still sign in to download their backups. Renew within 14 days to get everything back as it was. After 14 days your servers are deleted. A final backup of each is kept for 30 days, then deleted for good.</p>
<h2>No guarantees</h2>
<p>${name} runs the servers as well as it can, with no promise of uptime. Keep your own copies of the worlds you care about: backups can be downloaded from your dashboard. ${name} isn’t liable for lost worlds or data, or for more than you paid in the last month.</p>
<h2>Changes</h2>
<p>${name} may change these terms and says so on Whop when it does. Keeping your plan after a change means you accept it.</p>
<h2>Contact</h2>
<p>Message ${name} on Whop.</p>
</div>
</section>`,
  )
}

/** The owner's steps to open a store that isn't taking orders, one sentence and one button each. */
export function setupPage(s: Storefront) {
  return layout(
    { title: `Set up your store: ${nameOf(s)}`, description: 'Three steps to open your store.', store: s, noindex: true },
    html`<section class="band">
<div class="wrap narrow">
<h1 class="page-title">Set up your store</h1>
<p class="lead">Three steps, about two minutes.</p>
<ol class="setup">
${setupSteps(s).map((st) => html`<li>
<h2>${st.title}</h2>
<p>${st.text}</p>
${st.button}
</li>`)}
</ol>
<p class="fine">Stuck? <a href="${guide}">Read the guide</a>.</p>
</div>
</section>`,
  )
}

export function notFoundPage(s?: Storefront) {
  const name = nameOf(s)
  return layout(
    { title: `No page here: ${name}`, description: `${name}: Minecraft server hosting.`, ...(s ? { store: s } : {}) },
    html`<section class="band">
<div class="wrap narrow">
<h1 class="page-title">No page here</h1>
<p class="lead">That page doesn’t exist. <a href="/">See the plans</a>.</p>
</div>
</section>`,
  )
}

export function unavailablePage() {
  return layout(
    { title: `${fallbackName}: back soon`, description: 'The store couldn’t load its plans just now.' },
    html`<section class="band">
<div class="wrap narrow">
<h1 class="page-title">Back in a minute</h1>
<p class="lead">The store couldn’t load its plans from Whop just now. Try again in a minute.</p>
</div>
</section>`,
  )
}

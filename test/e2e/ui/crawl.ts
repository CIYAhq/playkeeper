import type { ElementHandle, Page, Request } from '@playwright/test'
import { breakControl, installPageHelpers, type ControlInfo, type Snapshot } from './crawl-page'
import { installFakes, type ApiCall, type View } from './fakes'

// Presses every control a person can reach and checks that each one visibly
// does something. See clickthrough.spec.ts for the rules.

export type Status =
  | 'works'
  | 'stays selected'
  | 'disabled with a reason'
  | 'dead'
  | 'broken'
  | 'disabled without a reason'
  | 'could not press'
  | 'no name'
  | 'busy'

export const failing: Status[] = ['dead', 'broken', 'disabled without a reason', 'could not press', 'no name']

export interface Result {
  viewport: string
  route: string
  /** The faked state the page was crawled in (fakes.ts), when it isn't the live one. */
  view?: View
  /** The controls pressed to reach the state this control was found in. */
  via: string[]
  key: string
  status: Status
  effects: string[]
  problems: string[]
  reason?: string
}

export interface CrawlReport {
  results: Result[]
  /** Time spent on each page, controls that went away before their turn, pages without a heading. */
  notes: string[]
  /** States the crawl found but could not get back to, so their controls went unpressed. */
  unreached: string[]
}

/** A page as the report names it: its route, and the faked state it was crawled in. */
export function where(route: string, view: View = 'live'): string {
  return view === 'live' ? route : `${route} (${view})`
}

const FILL = 'fill in the form'
const WINDOW_MS = 1600
const MAX_WAIT_MS = 7000
// A request cancelled by a page load while its route handler was still
// fetching never reports back, so old requests stop counting as in flight.
const STUCK_MS = 10_000

interface Req {
  method: string
  path: string
  first: boolean
  at: number
}

interface State {
  base: Snapshot
  /** The page before the last step opened the current menu or dialog. */
  under?: Snapshot
}

interface Tested {
  result: Result
  opened: boolean
  revealed: boolean
  /** The signature of the state pressing it led to. */
  leadsTo?: string
}

function diff(a: string[], b: string[]): string[] {
  const sa = new Set(a)
  const sb = new Set(b)
  return [...b.filter((x) => !sa.has(x)).map((x) => `+${x}`), ...a.filter((x) => !sb.has(x)).map((x) => `-${x}`)]
}

function sameState(a: Snapshot, b: Snapshot): boolean {
  return a.url === b.url && a.layers.join('|') === b.layers.join('|') && diff(a.fingerprint, b.fingerprint).length === 0
}

/**
 * What makes a state worth exploring: its page, open layers, headings and
 * controls. Which option is selected doesn't count, or every combination of
 * choices in a form would be a new state.
 */
function signature(s: Snapshot): string {
  const parts = s.fingerprint.filter((f) => f.startsWith('h:') || f.startsWith('c:')).map((f) => f.replace(/ \((selected|busy)\)/g, ''))
  return [s.url, ...s.layers, ...parts].sort().join('\n')
}

function isSelected(state: string | null): boolean {
  return !!state && /aria-(selected|checked|pressed)=true|data-checked=|data-pressed=/.test(state)
}

export class Crawler {
  readonly results: Result[] = []
  readonly notes: string[] = []
  readonly unreached: string[] = []
  /** What the panel's reads show (fakes.ts); crawl() sets it. */
  private view: View = 'live'
  private readonly tested = new Map<string, Tested>()
  /** States explored in any crawl so far: their controls have all been pressed, so a later page or view needn't open them again. */
  private readonly signatures = new Set<string>()
  /** Set while a negative control presses a control it broke, whose failure isn't the crawl's. */
  private quiet = false
  private calls: ApiCall[] = []
  private unfaked: string[] = []
  private reqs: Req[] = []
  private seen = new Set<string>()
  private pageErrors: string[] = []
  private consoleErrors: string[] = []
  private popups = 0
  private downloads = 0
  private choosers = 0
  private pending = new Map<Request, number>()
  private loadedAt = 0
  private loads = 0
  private unheaded = new Set<string>()

  constructor(
    private readonly page: Page,
    private readonly viewport: string,
    private readonly baseURL: string,
    private readonly log: (line: string) => void = () => {},
    private readonly maxDepth = 5,
  ) {}

  async init() {
    const { calls, unfaked } = await installFakes(this.page, this.baseURL, () => this.view)
    this.calls = calls
    this.unfaked = unfaked
    await this.page.addInitScript(installPageHelpers)
    const page = this.page
    page.on('pageerror', (e) => this.pageErrors.push(e.message))
    page.on('console', (m) => {
      if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) this.consoleErrors.push(m.text())
    })
    page.context().on('page', (p) => {
      this.popups++
      void p.close().catch(() => {})
    })
    page.on('download', (d) => {
      this.downloads++
      void d.cancel().catch(() => {})
    })
    page.on('filechooser', () => {
      this.choosers++
    })
    page.on('request', (r) => {
      const url = new URL(r.url())
      if (!url.pathname.startsWith('/api/')) return
      this.pending.set(r, Date.now())
      const key = `${r.method()} ${url.pathname}${url.search}`
      this.reqs.push({ method: r.method(), path: url.pathname, first: !this.seen.has(key), at: Date.now() })
      this.seen.add(key)
    })
    page.on('requestfinished', (r) => this.pending.delete(r))
    page.on('requestfailed', (r) => this.pending.delete(r))
  }

  /** API requests of the current page load that are still running. */
  private get inflight(): number {
    const now = Date.now()
    for (const [r, at] of this.pending) if (at < this.loadedAt || now - at > STUCK_MS) this.pending.delete(r)
    return this.pending.size
  }

  private async eval<T>(fn: () => T, fallback: T): Promise<T> {
    try {
      return await this.page.evaluate(fn)
    } catch {
      return fallback
    }
  }

  private async snap(withTarget = false): Promise<Snapshot | null> {
    try {
      return await this.page.evaluate((t) => window.__pk.snapshot(t), withTarget)
    } catch {
      return null
    }
  }

  private async controls(): Promise<ControlInfo[]> {
    return this.page.evaluate(() => window.__pk.controls()).catch(() => [])
  }

  private async handle(key: string): Promise<ElementHandle | null> {
    const h = await this.page.evaluateHandle((k) => window.__pk.element(k), key).catch(() => null)
    return h?.asElement() ?? null
  }

  /** The control with this key, once it has rendered. */
  private async find(key: string, timeout = 3000): Promise<ElementHandle | null> {
    const end = Date.now() + timeout
    for (;;) {
      const h = await this.handle(key)
      if (h || Date.now() > end) return h
      await this.page.waitForTimeout(150)
    }
  }

  /** Waits until requests have finished and short animations have run. */
  private async settle(max = 4000) {
    const end = Date.now() + max
    let quiet = 0
    while (Date.now() < end) {
      const running = await this.eval(() => window.__pk?.animationsRunning() ?? 0, 0)
      if (this.inflight === 0 && running === 0) {
        quiet ||= Date.now()
        if (Date.now() - quiet >= 200) return
      } else quiet = 0
      await this.page.waitForTimeout(50)
    }
  }

  private async ready() {
    // A heading in a placeholder that's still loading (aria-busy) doesn't count: the page's code is on its way.
    const headed = await this.page.waitForFunction(() => !!window.__pk && [...document.querySelectorAll('h1')].some((h) => !h.closest('[aria-busy="true"]')), null, { timeout: 20_000 }).then(
      () => true,
      () => false,
    )
    if (!headed) this.unheaded.add(new URL(this.page.url()).pathname)
    await this.page
      .waitForFunction(() => ![...document.querySelectorAll('[data-slot=skeleton]')].some((s) => (s as HTMLElement).checkVisibility()), null, { timeout: 15_000 })
      .catch(() => {})
    await this.settle()
  }

  /** Loads the route and presses the controls that lead to a state. */
  private async establish(route: string, path: string[]): Promise<State | null> {
    this.loads++
    this.seen.clear()
    this.loadedAt = Date.now()
    await this.page.goto(route, { waitUntil: 'domcontentloaded' })
    await this.ready()
    let under: Snapshot | undefined
    for (const step of path) {
      if (step === FILL) {
        await this.fill()
        await this.settle()
        continue
      }
      const h = await this.find(step)
      if (!h) return null
      const info = (await this.controls()).find((c) => c.key === step)
      await h.focus().catch(() => {})
      const before = await this.snap()
      if (await this.press(h, info)) return null
      await this.settle()
      const after = await this.snap()
      if (before && after && after.layers.length > before.layers.length) under = before
    }
    const base = await this.snap()
    return base ? { base, under } : null
  }

  /** Replays the steps to a state, once more if the page was still busy the first time. */
  private async reach(route: string, path: string[]): Promise<State | null> {
    return (await this.establish(route, path)) ?? (await this.establish(route, path))
  }

  /** Types sample values into the empty fields of the open form or page. */
  private async fill() {
    const fields = await this.page.evaluateHandle(() => {
      const scope = [...document.querySelectorAll('[role=dialog],[role=alertdialog]')].filter((d) => !document.getElementById('root')?.contains(d)).at(-1) ?? document.body
      return [...scope.querySelectorAll('input, textarea')].filter((i) => {
        const input = i as HTMLInputElement
        return !input.value && !input.readOnly && !input.disabled && input.type !== 'hidden' && input.type !== 'search' && input.type !== 'checkbox' && input.type !== 'radio' && input.type !== 'range' && input.type !== 'file' && input.getAttribute('role') !== 'combobox' && input.checkVisibility()
      })
    })
    const n = await fields.evaluate((a) => a.length)
    for (let i = 0; i < n; i++) {
      const el = (await fields.evaluateHandle((a, j) => a[j], i)).asElement()
      if (!el) continue
      const hint = await el.evaluate((input) => {
        const i = input as HTMLInputElement
        const labels = i.labels ? [...i.labels] : []
        const label = labels.map((l) => l.textContent).join(' ')
        // A typed confirmation says what to type in bold: "Type <b>replace world</b> to confirm."
        const phrase = /confirm/i.test(label) ? (labels.map((l) => l.querySelector('strong, b')?.textContent?.trim()).find(Boolean) ?? '') : ''
        return { type: i.type, numeric: i.inputMode === 'numeric', text: `${i.name} ${i.id} ${i.placeholder} ${i.getAttribute('aria-label') ?? ''} ${label}`.toLowerCase(), min: i.min, max: i.max, phrase }
      })
      let value = 'Sample'
      if (hint.phrase) value = hint.phrase
      else if (hint.type === 'password') value = 'sample-password-2026'
      else if (hint.type === 'number') value = hint.min || '1'
      // A code's boxes drop anything but digits; the first takes the whole code.
      else if (hint.numeric) value = '123456'
      else if (/webhook/.test(hint.text)) value = `https://discord.com/api/webhooks/123456789012345678/${'sample_token_'.repeat(6)}`
      else if (/minecraft|player|username|friend/.test(hint.text)) value = 'Pixel_Pia'
      else if (/command/.test(hint.text)) value = 'list'
      else if (/note/.test(hint.text)) value = 'Before the update'
      else if (/code/.test(hint.text)) value = 'ABCD-EFGH'
      else if (/name/.test(hint.text)) value = 'Sample world'
      await el.fill(value).catch(() => {})
    }
  }

  /** Presses a control like a person would. Returns why it could not be pressed. */
  private async press(h: ElementHandle, info?: ControlInfo): Promise<string | undefined> {
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        if (info?.role === 'slider') {
          await h.focus()
          const value = () => h.evaluate((el) => (el as HTMLInputElement).value)
          const was = await value()
          await this.page.keyboard.press('ArrowRight')
          await this.page.waitForTimeout(100)
          // A slider at its highest value can only go down.
          if ((await value()) === was) await this.page.keyboard.press('ArrowLeft')
        } else {
          await h.click({ timeout: 4000 })
          if (info?.editable) await this.page.keyboard.press('ArrowDown')
        }
        return undefined
      } catch (e) {
        const msg = String((e as Error).message ?? e)
        const toast = /intercepts pointer events/.test(msg) && /toast/.test(msg)
        const why = msg
          .split('\n')
          .filter((l) => /intercepts pointer events|not visible|not enabled|not stable|detached|outside of the viewport/.test(l))
          .at(-1)
          ?.replace(/^\s*-\s*/, '')
          .trim()
        if (!toast || attempt === 2) return why ?? msg.split('\n')[0]
        // A toast is in the way; it goes away on its own.
        await this.page.mouse.move(0, 0)
        await this.page.waitForFunction(() => document.querySelectorAll('[data-slot=toast-title]').length === 0, null, { timeout: 12_000 }).catch(() => {})
      }
    }
    return 'could not press'
  }

  private effects(c: ControlInfo, before: Snapshot, after: Snapshot | null, state: State, since: number, marks: { popups: number; downloads: number; choosers: number }): string[] {
    const out: string[] = []
    if (this.popups > marks.popups) out.push('opened a new tab')
    if (this.downloads > marks.downloads) out.push('started a download')
    if (this.choosers > marks.choosers) out.push('opened a file picker')
    for (const r of this.reqs) {
      if (r.at < since) continue
      if (r.method !== 'GET' && r.method !== 'HEAD') out.push(`sent ${r.method} ${r.path}`)
      else if (r.first) out.push(`loaded ${r.path}`)
    }
    if (!after) {
      out.push('navigated')
      return out
    }
    // A menu item or list option closes its menu; compare with the page under it.
    const ref = c.autoClose && state.under && after.layers.length < before.layers.length ? state.under : before
    if (after.url !== ref.url) out.push(`navigated to ${after.url}`)
    if (after.clip > before.clip) out.push('copied to the clipboard')
    const opened = after.layers.filter((l) => !ref.layers.includes(l))
    const closed = ref.layers.filter((l) => !after.layers.includes(l))
    for (const l of opened) out.push(`opened ${l}`)
    if (!c.autoClose) for (const l of closed) out.push(`closed ${l}`)
    if (before.target !== null && after.target !== null && before.target !== after.target) out.push(`changed its state (${after.target.slice(0, 80)})`)
    if (after.focus && after.focus !== before.focus && !(c.autoClose && after.focus === state.under?.focus)) out.push(`moved focus to ${after.focus}`)
    if (Math.abs(after.scrollY - ref.scrollY) > 40) out.push('scrolled the page')
    for (const t of after.toasts) if (!before.toasts.includes(t)) out.push(`said "${t}"`)
    if (opened.length === 0 && (closed.length === 0 || c.autoClose)) {
      const d = diff(ref.fingerprint, after.fingerprint).filter((x) => !x.includes('[current]') || !c.autoClose)
      if (d.length) out.push(`changed the page (${d.slice(0, 3).join('; ').slice(0, 160)})`)
    }
    return [...new Set(out)]
  }

  private problems(since: number, errs: { page: number; console: number; calls: number; unfaked: number }): string[] {
    const out: string[] = []
    for (const e of this.pageErrors.slice(errs.page)) out.push(`page error: ${e}`)
    for (const e of this.consoleErrors.slice(errs.console)) out.push(`console error: ${e.slice(0, 200)}`)
    for (const c of this.calls.slice(errs.calls)) if (c.at >= since && c.status >= 400 && !c.expected) out.push(`${c.method} ${c.path} answered ${c.status}${c.error ? `: ${c.error}` : ''}`)
    for (const u of this.unfaked.slice(errs.unfaked)) out.push(`no fake for ${u}; the real panel was not called`)
    return out
  }

  private record(route: string, via: string[], c: ControlInfo, status: Status, effects: string[] = [], problems: string[] = [], reason?: string): Result {
    const r: Result = { viewport: this.viewport, route, view: this.view === 'live' ? undefined : this.view, via, key: c.key, status, effects, problems, reason }
    this.results.push(r)
    if (!this.quiet) this.log(`${failing.includes(status) ? '✗' : '✓'} [${this.viewport}] ${where(route, this.view)}${via.length ? ` › ${via.join(' › ')}` : ''} › ${c.key}: ${status}${effects.length ? ` — ${effects[0]}` : ''}${problems.length ? ` — ${problems[0]}` : ''}`)
    return r
  }

  /** Presses one control in an established state and says what happened. */
  private async test(route: string, via: string[], c: ControlInfo, state: State): Promise<Tested> {
    if (!c.name) {
      return { result: this.record(route, via, c, 'no name'), opened: false, revealed: false }
    }
    if (c.disabled) {
      const status: Status = c.reason ? 'disabled with a reason' : 'disabled without a reason'
      return { result: this.record(route, via, c, status, [], [], c.reason || undefined), opened: false, revealed: false }
    }
    // Another app (an authenticator, mail, the phone) takes these, and a headless browser has none to show.
    if (c.app) return { result: this.record(route, via, c, 'works', [`hands a ${c.app}: link to another app`]), opened: false, revealed: false }
    if (c.busy) {
      await this.page.waitForFunction((k) => !window.__pk.controls().find((x) => x.key === k)?.busy, c.key, { timeout: 8000 }).catch(() => {})
      const again = (await this.controls()).find((x) => x.key === c.key)
      if (again?.busy) return { result: this.record(route, via, c, 'busy'), opened: false, revealed: false }
    }
    const h = await this.handle(c.key)
    if (!h) return { result: this.record(route, via, c, 'could not press', [], ['it went away before it could be pressed']), opened: false, revealed: false }
    await h.focus().catch(() => {})
    const before = await this.snap(true)
    if (!before) return { result: this.record(route, via, c, 'could not press', [], ['the page was not ready']), opened: false, revealed: false }
    const marks = { popups: this.popups, downloads: this.downloads, choosers: this.choosers }
    const errs = { page: this.pageErrors.length, console: this.consoleErrors.length, calls: this.calls.length, unfaked: this.unfaked.length }
    const keysBefore = new Set((await this.controls()).map((x) => x.key))
    const since = Date.now()
    const failed = await this.press(h, c)
    if (failed) return { result: this.record(route, via, c, 'could not press', [], [failed]), opened: false, revealed: false }
    let effects: string[]
    let after: Snapshot | null
    for (;;) {
      await this.page.waitForTimeout(120)
      after = await this.snap(true)
      effects = this.effects(c, before, after, state, since, marks)
      const waited = Date.now() - since
      if (effects.length || waited > MAX_WAIT_MS || (waited > WINDOW_MS && this.inflight === 0)) break
    }
    await this.settle(3000)
    await this.page.waitForTimeout(150)
    after = await this.snap(true)
    effects = [...new Set([...effects, ...this.effects(c, before, after, state, since, marks)])]
    const problems = this.problems(since, errs)
    let status: Status = 'works'
    if (problems.length) status = 'broken'
    else if (!effects.length) status = c.selected && (after === null || after.target === null || isSelected(after.target)) ? 'stays selected' : 'dead'
    // A menu item that opens a dialog, or a dialog that goes on to its next step, replaces the top layer.
    const replaced = !!after && after.layers.length > 0 && after.layers.length === before.layers.length && after.layers.at(-1) !== before.layers.at(-1)
    const opened = !!after && after.url === before.url && (after.layers.length > before.layers.length || replaced)
    let revealed = false
    if (!opened && after && after.url === before.url && after.layers.join('|') === before.layers.join('|')) {
      const now = await this.controls()
      revealed = now.some((x) => !keysBefore.has(x.key) && !x.isTarget)
    }
    return { result: this.record(route, via, c, status, effects, problems), opened, revealed, leadsTo: after ? signature(after) : undefined }
  }

  /** Puts the page back into the state, cheaply if it can. */
  private async restore(state: State): Promise<boolean> {
    let now = await this.snap()
    if (now && sameState(state.base, now)) return true
    if (now && now.url !== state.base.url && now.layers.length === 0 && state.base.layers.length === 0) {
      this.loadedAt = Date.now()
      await this.page.goBack({ waitUntil: 'commit' }).catch(() => null)
      // A page that replaced its history entry (signing out does) has nothing to go back to.
      const url = new URL(this.page.url())
      if (url.pathname + url.search + url.hash !== state.base.url) return false
      await this.ready()
      now = await this.snap()
      return !!now && sameState(state.base, now)
    }
    for (let i = 0; i < 2 && now && now.url === state.base.url && now.layers.length > state.base.layers.length; i++) {
      await this.page.keyboard.press('Escape')
      await this.settle()
      now = await this.snap()
    }
    return !!now && sameState(state.base, now)
  }

  /**
   * Crawls every state reachable from a route, with the panel's reads showing
   * `view`. A control already pressed in another view isn't pressed again.
   */
  async crawl(route: string, view: View = 'live') {
    this.view = view
    const started = Date.now()
    const count = this.results.length
    const loads = this.loads
    await this.explore(route)
    this.notes.push(`[${this.viewport}] ${where(route, view)}: ${this.results.length - count} controls in ${Math.round((Date.now() - started) / 1000)} s, ${this.loads - loads} page loads`)
    for (const p of this.unheaded) this.notes.push(`[${this.viewport}] ${where(p, view)}: no page heading (h1) after 20 s`)
    this.unheaded.clear()
  }

  /**
   * A negative control: presses a control the crawl found again, in the same
   * state, with the control made to do nothing when pressed (or, with
   * `unexplained`, a disabled control stripped of its reason). The verdict
   * must be a failing one, or the crawl can't tell a broken control there
   * from a working one. The result isn't added to the crawl's results.
   */
  async breakAndPress(found: Result, how: 'does nothing' | 'unexplained' = 'does nothing'): Promise<Result | string> {
    this.view = found.view ?? 'live'
    const sabotage = await this.page.addInitScript(breakControl, { key: found.key, how })
    try {
      const state = await this.reach(found.route, found.via)
      if (!state) return `could not reach ${where(found.route, found.view)}${found.via.length ? ` › ${found.via.join(' › ')}` : ''} again`
      const c = (await this.controls()).find((x) => x.key === found.key)
      if (!c) return `${found.key} wasn't there again`
      const count = this.results.length
      this.quiet = true
      const t = await this.test(found.route, found.via, c, state)
      this.results.splice(count)
      return t.result
    } finally {
      this.quiet = false
      await sabotage.dispose()
    }
  }

  private async explore(route: string) {
    const queue: string[][] = [[]]
    const signatures = this.signatures
    const queued = new Set<string>()
    const explored = new Set<string>()
    const enqueue = (path: string[], leadsTo?: string) => {
      if (path.length > this.maxDepth) return
      if (leadsTo && (signatures.has(leadsTo) || queued.has(leadsTo))) return
      if (leadsTo) queued.add(leadsTo)
      queue.push(path)
    }
    while (queue.length) {
      const path = queue.shift() as string[]
      let state = await this.reach(route, path)
      if (!state) {
        this.unreached.push(`[${this.viewport}] ${where(route, this.view)}: could not reach ${path.join(' › ') || 'the page'} again`)
        continue
      }
      const sig = signature(state.base)
      // The page itself is gone through every time, since the menus and dialogs it opens may hold different controls in another view.
      if (signatures.has(sig) && path.length > 0) continue
      signatures.add(sig)
      const list = await this.controls()
      const endsWithReveal = path.length > 0 && path.at(-1) !== FILL && !!this.tested.get(path.at(-1) as string)?.revealed
      // Counted in the state itself: by the end of the loop a dialog with the form may have closed.
      const fillable = path.at(-1) !== FILL && list.some((c) => c.disabled) ? await this.eval(() => window.__pk.fillable(), 0) : 0
      let dirty = false
      for (const c of list) {
        const done = this.tested.get(c.key)
        const again = !!done && !c.disabled && ((done.opened && !explored.has(c.key)) || (done.revealed && endsWithReveal && path.length < this.maxDepth))
        if (done && !again) continue
        if (dirty) {
          state = await this.reach(route, path)
          if (!state) {
            this.unreached.push(`[${this.viewport}] ${where(route, this.view)}: could not reach ${path.join(' › ') || 'the page'} again`)
            break
          }
          dirty = false
        }
        const fresh = (await this.controls()).find((x) => x.key === c.key)
        if (!fresh) {
          if (!done) this.notes.push(`[${this.viewport}] ${where(route, this.view)}${path.length ? ` › ${path.join(' › ')}` : ''}: ${c.key} went away before it was pressed`)
          continue
        }
        const t = await this.test(route, path, fresh, state)
        if (done) this.results.pop()
        else this.tested.set(c.key, t)
        if (t.opened) {
          explored.add(c.key)
          if (done) done.opened = true
          enqueue([...path, c.key], t.leadsTo)
        } else if (t.revealed) {
          if (done) done.revealed = true
          enqueue([...path, c.key], t.leadsTo)
        }
        dirty = !(await this.restore(state))
      }
      if (fillable > 0) enqueue([...path, FILL])
    }
  }
}

/** A readable list of the controls that failed, grouped by page. */
export function failureList(results: Result[]): string {
  const bad = results.filter((r) => failing.includes(r.status))
  if (!bad.length) return ''
  const lines: string[] = [`${bad.length} control${bad.length === 1 ? '' : 's'} failed:`]
  let last = ''
  for (const r of bad) {
    const at = `${r.viewport} ${where(r.route, r.view)}${r.via.length ? ` › ${r.via.join(' › ')}` : ''}`
    if (at !== last) lines.push(`\n  ${at}`)
    last = at
    const why = r.status === 'dead' ? 'pressing it did nothing visible' : r.status === 'broken' ? r.problems.join('; ') : r.status === 'could not press' ? r.problems.join('; ') : r.status === 'disabled without a reason' ? 'disabled, but nothing says why' : 'has no accessible name'
    lines.push(`    ✗ ${r.key}: ${why}`)
  }
  return lines.join('\n')
}

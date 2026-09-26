// Runs inside the page (installed with addInitScript), so it must not use
// anything from its module scope. It finds the controls a person could press,
// names them the way assistive technology would, and takes small snapshots
// of the page so the crawler can tell whether pressing a control did anything.

export interface ControlInfo {
  /** Stable within one visit: role, name, context and occurrence. */
  key: string
  role: string
  name: string
  disabled: boolean
  reason: string
  busy: boolean
  /** Selected member of a single-choice group (tab, radio, option, pressed toggle). */
  selected: boolean
  /** Closes its own menu or list when pressed (menu items, list options). */
  autoClose: boolean
  editable: boolean
  /** The control that was pressed last. */
  isTarget: boolean
  /** The scheme of a link another app opens (otpauth, mailto, tel, sms), else empty. */
  app: string
}

export interface Snapshot {
  url: string
  scrollY: number
  layers: string[]
  focus: string
  target: string | null
  fingerprint: string[]
  toasts: string[]
  clip: number
}

declare global {
  interface Window {
    __pk: {
      controls(): ControlInfo[]
      element(key: string): Element | null
      /** The control with this key, without making it the one pressed last. */
      find(key: string): Element | null
      /** The key of the control a press on `el` lands on. */
      keyOf(el: Element): string | undefined
      snapshot(withTarget?: boolean): Snapshot
      animationsRunning(): number
      fillable(): number
    }
  }
}

export function installPageHelpers() {
  const ROLES = new Set(['button', 'link', 'switch', 'checkbox', 'radio', 'tab', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'option', 'combobox', 'slider'])
  const LAYER_ROLES = new Set(['dialog', 'alertdialog', 'menu', 'listbox'])
  const clips: string[] = []

  const wrapClipboard = () => {
    const cb = navigator.clipboard as Clipboard | undefined
    if (cb && !(cb as unknown as { __pk?: boolean }).__pk) {
      Object.defineProperty(cb, 'writeText', {
        configurable: true,
        value: (text: string) => {
          clips.push(String(text))
          return Promise.resolve()
        },
      })
      ;(cb as unknown as { __pk?: boolean }).__pk = true
    }
  }
  wrapClipboard()
  const exec = document.execCommand.bind(document)
  document.execCommand = (cmd: string, ...rest: unknown[]) => {
    if (cmd.toLowerCase() === 'copy') {
      clips.push(String(window.getSelection() ?? ''))
      return true
    }
    return exec(cmd, ...(rest as [boolean, string]))
  }

  const norm = (s: string | null | undefined, max = 80) =>
    (s ?? '')
      .replace(/\s+/g, ' ')
      .trim()
      .replace(/\d+/g, '#')
      .slice(0, max)

  function roleOf(el: Element): string {
    const explicit = el.getAttribute('role')
    if (explicit) return explicit.split(' ')[0] ?? ''
    const tag = el.tagName.toLowerCase()
    if (tag === 'a' && el.hasAttribute('href')) return 'link'
    if (tag === 'button' || tag === 'summary') return 'button'
    if (tag === 'select') return 'combobox'
    if (tag === 'input') {
      const type = (el as HTMLInputElement).type
      if (type === 'checkbox') return 'checkbox'
      if (type === 'radio') return 'radio'
      if (type === 'range') return 'slider'
      if (type === 'button' || type === 'submit' || type === 'reset' || type === 'image') return 'button'
    }
    return ''
  }

  function textOf(el: Element): string {
    if (el instanceof HTMLElement) return el.innerText || el.textContent || ''
    return el.textContent ?? ''
  }

  function byIds(ids: string | null): string {
    if (!ids) return ''
    return ids
      .split(/\s+/)
      .map((id) => document.getElementById(id))
      .filter((x): x is HTMLElement => !!x)
      .map((x) => x.textContent ?? '')
      .join(' ')
  }

  function nameOf(el: Element): string {
    const labelled = byIds(el.getAttribute('aria-labelledby'))
    if (labelled.trim()) return labelled
    const aria = el.getAttribute('aria-label')
    if (aria?.trim()) return aria
    if (el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement) {
      const labels = el.labels ? [...el.labels].map((l) => l.textContent ?? '').join(' ') : ''
      if (labels.trim()) return labels
      if (el instanceof HTMLInputElement && (el.type === 'button' || el.type === 'submit')) return el.value
    }
    const text = textOf(el)
    if (text.trim()) return text
    const img = el.querySelector('img[alt], svg[aria-label]')
    const alt = img?.getAttribute('alt') ?? img?.getAttribute('aria-label')
    if (alt?.trim()) return alt
    return el.getAttribute('title') ?? ''
  }

  function hiddenFromAT(el: Element): boolean {
    return !!el.closest('[inert], [aria-hidden="true"]')
  }

  function box(el: Element): DOMRect {
    let r = el.getBoundingClientRect()
    if ((r.width < 4 || r.height < 4) && el instanceof HTMLInputElement && el.parentElement) r = el.parentElement.getBoundingClientRect()
    return r
  }

  function clipped(el: Element): boolean {
    const s = getComputedStyle(el)
    return s.clip === 'rect(0px, 0px, 0px, 0px)' || s.clipPath === 'inset(50%)'
  }

  /** What a person clicks: the control, the label around a visually hidden one, or a slider's thumb around its hidden input. */
  function proxyOf(el: Element): Element | null {
    if (!clipped(el) && box(el).width >= 4 && box(el).height >= 4) return el
    const label = el.closest('label') ?? (el.id ? document.querySelector(`label[for="${CSS.escape(el.id)}"]`) : null)
    if (label && !clipped(label)) return label
    const thumb = el.closest('[data-slot=slider-thumb]')
    return thumb && !clipped(thumb) ? thumb : null
  }

  function visible(el: Element): boolean {
    if (!el.isConnected || hiddenFromAT(el)) return false
    const shown = proxyOf(el)
    if (!shown) return false
    const r = box(shown)
    if (r.width < 4 || r.height < 4) return false
    return shown.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true } as CheckVisibilityOptions)
  }

  const root = () => document.getElementById('root')

  function layers(): Element[] {
    const found: Element[] = []
    for (const el of document.querySelectorAll('[role]')) {
      const role = el.getAttribute('role') ?? ''
      if (!LAYER_ROLES.has(role)) continue
      if (root()?.contains(el)) continue
      if (!visible(el)) continue
      if (found.some((f) => f.contains(el))) continue
      if (el.closest('[data-ending-style]')) continue
      found.push(el)
    }
    return found
  }

  function layerLabel(el: Element): string {
    const role = el.getAttribute('role') ?? ''
    const label = byIds(el.getAttribute('aria-labelledby')) || el.getAttribute('aria-label') || el.querySelector('h1,h2,h3,[data-slot$=title]')?.textContent || ''
    return `${role} "${norm(label, 40)}"`
  }

  function scopeRoot(): Element {
    const ls = layers()
    return ls.at(-1) ?? document.body
  }

  function contextOf(el: Element): string {
    const parts: string[] = []
    for (let p = el.parentElement; p; p = p.parentElement) {
      const role = p.getAttribute('role') ?? ''
      if (LAYER_ROLES.has(role) && !root()?.contains(p)) {
        parts.unshift(layerLabel(p))
        break
      }
      const tag = p.tagName.toLowerCase()
      if ((tag === 'nav' || tag === 'aside' || tag === 'form' || role === 'navigation' || role === 'toolbar' || role === 'tablist' || role === 'radiogroup' || role === 'group') && (p.getAttribute('aria-label') || p.getAttribute('aria-labelledby'))) {
        parts.unshift(`${role || tag} "${norm(byIds(p.getAttribute('aria-labelledby')) || p.getAttribute('aria-label'), 40)}"`)
        continue
      }
      if (parts.length === 0 && (tag === 'section' || tag === 'form' || tag === 'fieldset' || tag === 'li' || tag === 'tr' || p.getAttribute('data-slot') === 'card')) {
        const h = p.querySelector('h1,h2,h3,h4,legend,[data-slot=card-title]')
        if (h && !h.contains(el)) {
          parts.unshift(`"${norm(h.textContent, 40)}"`)
        } else if (tag === 'li' || tag === 'tr') {
          const first = norm(textOf(p).split('\n')[0], 30)
          if (first && first !== norm(nameOf(el), 30)) parts.unshift(`row "${first}"`)
        }
      }
    }
    return parts.join(' > ')
  }

  function disabledOf(el: Element): boolean {
    // A button with a spinner is busy, not disabled; its spinner says why.
    if (el.hasAttribute('data-loading')) return false
    if ((el as HTMLButtonElement).disabled) return true
    if (el.matches(':disabled')) return true
    if (el.getAttribute('aria-disabled') === 'true' && !el.hasAttribute('data-loading')) return true
    if (el.hasAttribute('data-disabled')) return true
    return false
  }

  function reasonOf(el: Element): string {
    return (byIds(el.getAttribute('aria-describedby')) || el.getAttribute('aria-description') || el.getAttribute('title') || '').replace(/\s+/g, ' ').trim()
  }

  function selectedOf(el: Element, role: string): boolean {
    if (role === 'tab' || role === 'option') return el.getAttribute('aria-selected') === 'true'
    if (role === 'radio' || role === 'menuitemradio') return el.getAttribute('aria-checked') === 'true' || (el as HTMLInputElement).checked === true
    if (role === 'button' && el.getAttribute('aria-pressed') === 'true') {
      const group = el.closest('[role=group],[role=radiogroup],[role=toolbar],[data-slot=toggle-group]')
      return !!group && group.querySelectorAll('[aria-pressed]').length > 1
    }
    return false
  }

  let list: { key: string; el: Element }[] = []
  let target: Element | null = null

  function controls(): ControlInfo[] {
    wrapClipboard()
    const scope = scopeRoot()
    const out: ControlInfo[] = []
    const seen = new Map<string, number>()
    list = []
    const candidates = scope.querySelectorAll('a[href], button, summary, select, input, [role], [tabindex]')
    for (const el of candidates) {
      const role = roleOf(el)
      if (!ROLES.has(role)) continue
      if (el.closest('[data-slot=toast-viewport],[data-slot=toast-viewport-anchored]')) continue
      // Outside the app, only the phone's action bar is part of the page; anything else is a layer on its way out.
      if (scope === document.body && layers().length === 0 && !root()?.contains(el) && !el.closest('[data-slot=phone-action-bar]')) continue
      if (!visible(el)) continue
      const name = norm(nameOf(el), 60)
      const context = contextOf(el)
      const current = el.getAttribute('aria-current')
      const disabled = disabledOf(el)
      const base = `${role} "${name}"${context ? ` in ${context}` : ''}${current && current !== 'false' ? ' [current]' : ''}${disabled ? ' [disabled]' : ''}`
      const n = (seen.get(base) ?? 0) + 1
      seen.set(base, n)
      const key = n > 1 ? `${base} #${n}` : base
      const inMenu = !!el.closest('[role=menu]')
      const inPopupList = !!el.closest('[role=listbox]') && !root()?.contains(el)
      out.push({
        key,
        role,
        name,
        disabled,
        reason: disabled ? reasonOf(el) : '',
        busy: el.hasAttribute('data-loading') || el.getAttribute('aria-busy') === 'true',
        selected: selectedOf(el, role),
        autoClose: inMenu || (role === 'option' && inPopupList),
        editable: el instanceof HTMLInputElement && role === 'combobox',
        isTarget: el === target,
        app: el instanceof HTMLAnchorElement && ['otpauth:', 'mailto:', 'tel:', 'sms:'].includes(el.protocol) ? el.protocol.slice(0, -1) : '',
      })
      list.push({ key, el })
    }
    return out
  }

  function element(key: string): Element | null {
    controls()
    target = list.find((x) => x.key === key)?.el ?? null
    // A slider is moved with the keyboard, so it's the input that needs focus, not its thumb.
    return target && (roleOf(target) === 'slider' ? target : proxyOf(target))
  }

  function find(key: string): Element | null {
    controls()
    return list.find((x) => x.key === key)?.el ?? null
  }

  function keyOf(el: Element): string | undefined {
    controls()
    const inner = (hits: { key: string; el: Element }[]) => hits.reduce<{ key: string; el: Element } | undefined>((best, x) => (!best || best.el.contains(x.el) ? x : best), undefined)
    return (inner(list.filter((x) => x.el.contains(el))) ?? inner(list.filter((x) => proxyOf(x.el)?.contains(el))))?.key
  }

  function stateOf(el: Element): string {
    const attrs = ['aria-checked', 'aria-pressed', 'aria-expanded', 'aria-selected', 'aria-current', 'aria-disabled', 'aria-valuenow', 'aria-activedescendant', 'data-checked', 'data-unchecked', 'data-pressed', 'data-popup-open', 'data-panel-open', 'data-loading', 'data-state', 'data-open']
    const parts = attrs.map((a) => (el.hasAttribute(a) ? `${a}=${el.getAttribute(a)}` : '')).filter(Boolean)
    if ((el as HTMLButtonElement).disabled) parts.push('disabled')
    if (el instanceof HTMLInputElement) parts.push(`value=${el.type === 'checkbox' || el.type === 'radio' ? el.checked : el.value}`)
    if (el.tagName === 'SUMMARY' && el.parentElement instanceof HTMLDetailsElement) parts.push(`open=${el.parentElement.open}`)
    parts.push(`text=${norm(textOf(el), 80)}`)
    return parts.join(' ')
  }

  function describeFocus(): string {
    const a = document.activeElement
    if (!a || a === document.body || a === document.documentElement) return ''
    const role = roleOf(a) || a.tagName.toLowerCase()
    return `${role} "${norm(nameOf(a), 40)}" ${norm(a.id, 20)}`
  }

  const LIVE = '[role=status],[role=log],[role=progressbar],[role=meter],[role=timer],[data-slot=toast-viewport],[aria-live]'

  function fingerprint(): string[] {
    const scope = scopeRoot()
    const out: string[] = []
    for (const h of scope.querySelectorAll('h1,h2,h3,h4,[role=heading]')) if (visible(h) && !h.closest(LIVE)) out.push(`h:${norm(textOf(h), 60)}`)
    for (const c of controls()) out.push(`c:${c.key}${c.selected ? ' (selected)' : ''}${c.busy ? ' (busy)' : ''}`)
    for (const el of list) out.push(`s:${el.key}:${stateOf(el.el)}`)
    for (const a of scope.querySelectorAll('[role=alert]')) if (visible(a)) out.push(`alert:${norm(textOf(a), 80)}`)
    for (const i of scope.querySelectorAll('input:not([type=hidden]), textarea')) if (visible(i) || (i as HTMLInputElement).type === 'checkbox') out.push(`in:${(i as HTMLInputElement).name || (i as HTMLInputElement).id}:${(i as HTMLInputElement).type === 'checkbox' ? String((i as HTMLInputElement).checked) : norm((i as HTMLInputElement).value, 40)}`)
    for (const t of scope.querySelectorAll('table')) if (visible(t)) out.push(`table:${t.querySelectorAll('tbody tr').length}`)
    for (const l of scope.querySelectorAll('ul,ol,[role=list]')) if (visible(l) && !l.closest(LIVE)) out.push(`list:${norm(l.getAttribute('aria-label'), 20)}:${l.children.length}`)
    for (const d of scope.querySelectorAll('details')) out.push(`details:${(d as HTMLDetailsElement).open}`)
    for (const p of scope.querySelectorAll('[role=tabpanel]')) if (visible(p)) out.push(`panel:${norm(p.getAttribute('aria-labelledby') ?? p.id, 30)}`)
    for (const img of scope.querySelectorAll('canvas, svg[role=img], img')) if (visible(img)) out.push(`img:${norm(img.getAttribute('aria-label') ?? img.getAttribute('alt'), 30)}`)
    return out
  }

  function snapshot(withTarget?: boolean): Snapshot {
    wrapClipboard()
    const targetState = withTarget && target && target.isConnected && visible(target) ? stateOf(target) : null
    const toasts = [...document.querySelectorAll('[data-slot=toast-title], [data-slot=toast-description]')].map((x) => norm(x.textContent, 80)).filter(Boolean)
    return {
      url: location.pathname + location.search + location.hash,
      scrollY: Math.round(window.scrollY),
      layers: layers().map(layerLabel),
      focus: describeFocus(),
      target: targetState,
      fingerprint: fingerprint(),
      toasts,
      clip: clips.length,
    }
  }

  function animationsRunning(): number {
    return document.getAnimations().filter((a) => {
      const timing = a.effect?.getComputedTiming()
      return a.playState === 'running' && timing && timing.iterations !== Infinity && Number(timing.endTime) < 5000
    }).length
  }

  function fillable(): number {
    const scope = scopeRoot()
    return [...scope.querySelectorAll('input, textarea')].filter((i) => {
      const input = i as HTMLInputElement
      return ['text', 'password', 'search', 'email', 'url', 'number', 'textarea', ''].includes(input.type) && input.type !== 'search' && !input.value && !input.readOnly && !input.disabled && visible(input) && roleOf(input) !== 'combobox'
    }).length
  }

  window.__pk = { controls, element, find, keyOf, snapshot, animationsRunning, fillable }
}

/**
 * Also runs inside the page, for negative controls (Crawler.breakAndPress).
 * 'does nothing': pressing the control with this key does nothing, like a
 * button whose handler is missing. 'unexplained': the disabled control loses
 * the description that says why.
 */
export function breakControl({ key, how }: { key: string; how: 'does nothing' | 'unexplained' }) {
  if (how === 'does nothing') {
    const stop = (e: Event) => {
      if (e.target instanceof Element && window.__pk?.keyOf(e.target) === key) {
        e.stopImmediatePropagation()
        e.preventDefault()
      }
    }
    for (const type of ['pointerdown', 'pointerup', 'mousedown', 'mouseup', 'click', 'keydown', 'keyup', 'touchstart', 'touchend']) window.addEventListener(type, stop, true)
    return
  }
  const strip = () => {
    const el = window.__pk?.find(key)
    if (el) for (const a of ['aria-describedby', 'aria-description', 'title']) el.removeAttribute(a)
  }
  new MutationObserver(strip).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ['aria-describedby', 'aria-description', 'title'] })
}

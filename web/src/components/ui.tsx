import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import type { Phase } from '../api/types'
import { phaseLabel, phaseTone } from '../lib/phase'

export function Logo({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
      <rect x="2" y="2" width="20" height="20" rx="6" fill="#15803d" />
      <path d="M7 16.5V7.5h5.2a3.1 3.1 0 0 1 0 6.2H9.6" fill="none" stroke="#fff" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
      <circle cx="16.6" cy="16.2" r="1.6" fill="#bbf7d0" />
    </svg>
  )
}

const icons: Record<string, string> = {
  overview: 'M3 12l9-8 9 8v8a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z',
  console: 'M4 5h16v14H4zM7 9l3 3-3 3M12 15h5',
  players: 'M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM2.5 20a6.5 6.5 0 0 1 13 0M16 4.5a3.5 3.5 0 0 1 0 6.5M18 14a6 6 0 0 1 3.5 6',
  world: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM3 12h18M12 3c2.5 2.7 3.8 5.7 3.8 9s-1.3 6.3-3.8 9c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
  alert: 'M12 9v4M12 17h.01M10.3 3.9L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
  info: 'M12 16v-4M12 8h.01M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z',
  check: 'M20 6L9 17l-5-5',
}

export function Icon({ name, className }: { name: keyof typeof icons | string; className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={icons[name] ?? icons.info} />
    </svg>
  )
}

export function StatusPill({ phase, label }: { phase: Phase; label?: string }) {
  return <span className={`pill ${phaseTone(phase)}`}>{label ?? phaseLabel[phase]}</span>
}

export function Banner({ tone = 'info', title, children, action }: { tone?: 'info' | 'warn' | 'bad' | 'busy' | 'good'; title: ReactNode; children?: ReactNode; action?: ReactNode }) {
  const role = tone === 'bad' || tone === 'warn' ? 'alert' : 'status'
  return (
    <div className={`banner ${tone === 'info' ? '' : tone}`} role={role}>
      {tone === 'busy' ? <span className="spinner" aria-hidden="true" /> : <Icon name={tone === 'bad' || tone === 'warn' ? 'alert' : tone === 'good' ? 'check' : 'info'} className="icon" />}
      <div className="body">
        <div className="title">{title}</div>
        {children && <div className="hint">{children}</div>}
      </div>
      {action}
    </div>
  )
}

export function Card({ title, hint, actions, children, labelledBy }: { title?: ReactNode; hint?: ReactNode; actions?: ReactNode; children: ReactNode; labelledBy?: string }) {
  const id = useId()
  return (
    <section className="card" aria-labelledby={title ? labelledBy ?? id : undefined}>
      {(title || actions) && (
        <div className="card-head">
          <div>
            {title && <h2 id={labelledBy ?? id}>{title}</h2>}
            {hint && <div className="hint">{hint}</div>}
          </div>
          {actions}
        </div>
      )}
      {children}
    </section>
  )
}

export function Stat({ label, value, meta }: { label: string; value: ReactNode; meta?: ReactNode }) {
  return (
    <div className="card stat">
      <span className="label">{label}</span>
      <span className="value">{value}</span>
      {meta && <span className="meta">{meta}</span>}
    </div>
  )
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <span className="title">{title}</span>
      {children && <span>{children}</span>}
    </div>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    setDone(true)
    window.setTimeout(() => setDone(false), 1800)
  }
  return (
    <button type="button" className="btn small" onClick={copy} aria-live="polite">
      {done ? 'Copied' : label}
    </button>
  )
}

export function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: { value: T; label: string }[]; onChange: (v: T) => void; label: string }) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

/** Modal built on <dialog>: the browser traps focus and Esc closes it. */
export function Modal({ open, onClose, title, children }: { open: boolean; onClose: () => void; title: string; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null)
  const id = useId()
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (open && !d.open) d.showModal()
    if (!open && d.open) d.close()
  }, [open])
  return (
    <dialog ref={ref} aria-labelledby={id} onClose={onClose} onCancel={onClose}>
      {open && (
        <div className="dialog-body">
          <h2 id={id}>{title}</h2>
          {children}
        </div>
      )}
    </dialog>
  )
}

export function Spinner({ label }: { label?: string }) {
  return (
    <span className="muted">
      <span className="spinner" aria-hidden="true" /> {label ?? 'Loading…'}
    </span>
  )
}

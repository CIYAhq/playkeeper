import { useEffect, useState, type ReactNode } from 'react'
import { BookOpenIcon, ChevronRightIcon, CircleCheckIcon, CircleXIcon, CopyIcon, DownloadIcon, EllipsisIcon, ExternalLinkIcon, FileDownIcon, HardDriveIcon, HourglassIcon, KeyRoundIcon, PowerOffIcon, RefreshCwIcon, ServerIcon, ShieldCheckIcon, TriangleAlertIcon } from 'lucide-react'
import { ApiError, download, get, post } from '@/api/client'
import type { OffsiteCheck, OffsiteCopy, OffsiteNewKey, OffsitePending, OffsiteTestResult, OffsiteView, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, CopyButton, Marker, Progress, SectionLabel, Spinner, copyText } from '@/components/app/bits'
import { CardGroup, ChoiceCard, ChoiceSelect, Segmented } from '@/components/app/controls'
import { PhoneBackHeader } from '@/components/app/shell'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuLinkItem, MenuPopup, MenuTrigger } from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { can } from '@/lib/access'
import { formatBytes, formatClock, formatDate, formatPercent, formatSpan, relativeTime } from '@/lib/format'
import { navigate } from '@/lib/router'
import { cn } from '@/lib/utils'
import { useOffsite } from './backups'

const ntpCommand = 'sudo timedatectl set-ntp true'
const savedDots = '•'.repeat(16)

type Dest = 's3' | 'sftp'
type Auth = 'key' | 'password'

interface Draft {
  type: Dest
  endpoint: string
  bucket: string
  accessKeyId: string
  secretKey: string
  host: string
  port: string
  user: string
  folder: string
  auth: Auth
  password: string
  hostKey: string
}

export type HostKey = NonNullable<OffsiteTestResult['hostKey']>
interface Changed {
  confirmed: string
  now: string
  key?: HostKey
}
export interface Problem {
  msg: string
  hint?: string
  field?: string
}
/** A change of place waiting for the user to agree to forget the copies at the old one. */
interface Forget {
  place: string
  count: number
  onlyThere: number
  /** The copies whose backup is gone from this machine, when the list could be read. */
  only: OffsiteCopy[]
  answer: (ok: boolean) => void
}
type Busy = 'test' | 'save' | 'on' | 'off' | 'retry' | 'key' | 'newKey'

function draftOf(v: OffsiteView): Draft {
  return {
    type: v.type === 'sftp' ? 'sftp' : 's3',
    endpoint: (v.s3?.endpoint ?? '').replace(/^https:\/\//, ''),
    bucket: v.s3?.bucket ?? '',
    accessKeyId: v.s3?.accessKeyId ?? '',
    secretKey: '',
    host: v.sftp?.host ?? '',
    port: String(v.sftp?.port || 22),
    user: v.sftp?.user ?? '',
    folder: v.sftp?.folder ?? '',
    auth: v.sftp?.auth === 'password' ? 'password' : 'key',
    password: '',
    hostKey: '',
  }
}

function isDirty(d: Draft, v: OffsiteView): boolean {
  if (!v.configured) return true
  const saved = draftOf(v)
  if (d.type !== saved.type || d.secretKey || d.password || d.hostKey) return true
  const keys: (keyof Draft)[] = d.type === 's3' ? ['endpoint', 'bucket', 'accessKeyId'] : ['host', 'port', 'user', 'folder', 'auth']
  return keys.some((k) => d[k].trim() !== saved[k].trim())
}

/** The settings on the page as the agent takes them; a secret left empty keeps the saved one. */
function request(d: Draft, v: OffsiteView): Record<string, unknown> {
  if (d.type === 's3') {
    const body: Record<string, unknown> = { config: { type: 's3', s3: { endpoint: d.endpoint.trim(), bucket: d.bucket.trim(), accessKeyId: d.accessKeyId.trim(), prefix: v.s3?.prefix ?? '' } } }
    if (d.secretKey.trim()) body.secretKey = d.secretKey.trim()
    return body
  }
  const body: Record<string, unknown> = { config: { type: 'sftp', sftp: { host: d.host.trim(), port: Number(d.port) || 22, user: d.user.trim(), folder: d.folder.trim() } }, sftpAuth: d.auth }
  if (d.auth === 'password' && d.password) body.password = d.password
  if (d.hostKey) body.hostKey = d.hostKey
  return body
}

function hostOf(endpoint: string): string {
  try {
    return new URL(endpoint.includes('://') ? endpoint : `https://${endpoint}`).hostname
  } catch {
    return endpoint
  }
}

/** "Backblaze B2" for an endpoint the agent knows, else its host name. */
function placeOf(d: Draft, v: OffsiteView): string {
  if (d.type === 'sftp') return d.host.trim()
  const host = hostOf(d.endpoint.trim())
  const known = v.providers.find((p) => {
    const tmpl = p.endpoint.replace(/^https?:\/\//, '')
    const i = tmpl.lastIndexOf('}')
    return p.id !== 'other' && i >= 0 && host.endsWith(tmpl.slice(i + 2))
  })
  return known?.name ?? host
}

function problemOf(e: unknown): Problem {
  return e instanceof ApiError ? { msg: e.message, hint: e.hint, field: e.field } : { msg: errorText(e) }
}

function daysAgo(iso: string): number {
  const d = new Date(iso)
  const now = new Date()
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  return Math.round((start(now) - start(d)) / 86_400_000)
}

/** "today 18:47", "yesterday 21:14" or "22 Sep 20:30". */
function whenPhrase(iso: string): string {
  const n = daysAgo(iso)
  if (n === 0) return t('offsite.day.today', { time: formatClock(iso) })
  if (n === 1) return t('offsite.day.yesterday', { time: formatClock(iso) })
  return `${formatDate(iso)} ${formatClock(iso)}`
}

function copyingTitle(p: OffsitePending): string {
  const at = p.backupCreatedAt
  if (!at) return t('offsite.copying.next')
  const n = daysAgo(at)
  if (n === 0) return t('offsite.copying.today', { time: formatClock(at) })
  if (n === 1) return t('offsite.copying.yesterday', { time: formatClock(at) })
  return t('offsite.copying.date', { date: formatDate(at) })
}

const checkLabels: Record<Dest, Record<string, MessageKey>> = {
  sftp: {
    connect: 'offsite.check.sftp.connect',
    folder: 'offsite.check.sftp.folder',
    write: 'offsite.check.sftp.write',
    rename: 'offsite.check.sftp.rename',
    read: 'offsite.check.sftp.read',
    list: 'offsite.check.sftp.list',
    delete: 'offsite.check.sftp.delete',
  },
  s3: {
    write: 'offsite.check.s3.write',
    read: 'offsite.check.s3.read',
    list: 'offsite.check.s3.list',
    multipart: 'offsite.check.s3.multipart',
    delete: 'offsite.check.s3.delete',
  },
}

function checkLabel(type: Dest, c: OffsiteCheck, user: string): string {
  const key = checkLabels[type][c.step]
  return key ? t(key, { user }) : c.msg
}

const clockLimit = 5 * 60e9

function testTone(r: OffsiteTestResult): 'passed' | 'warning' | 'failed' {
  if (!r.ok) return 'failed'
  return Math.abs(r.skew) >= clockLimit || r.warning ? 'warning' : 'passed'
}

function hostKeyKind(type: string): { label: string; file: string } {
  if (type.includes('ed25519')) return { label: 'ED25519', file: 'ed25519' }
  if (type.startsWith('ecdsa')) return { label: 'ECDSA', file: 'ecdsa' }
  if (type.includes('rsa')) return { label: 'RSA', file: 'rsa' }
  return { label: type, file: 'ed25519' }
}

/** The authorized_keys line with the middle of the key left out. */
function shortKeyLine(line: string): string {
  return line
    .split(' ')
    .map((part) => (part.startsWith('AAAA') && part.length > 40 ? `${part.slice(0, 26)}…${part.slice(-4)}` : part))
    .join(' ')
}

type CopyState =
  | { kind: 'off' }
  | { kind: 'idle' }
  | { kind: 'copying'; p: OffsitePending }
  | { kind: 'waiting'; p: OffsitePending }
  | { kind: 'failed'; p: OffsitePending }
  | { kind: 'stopped'; p: OffsitePending }

// Kinds the uploader gets past by itself by trying again later.
const resumable = new Set(['network', 'rate_limited', 'service_error', 'canceled', 'upload_gone'])

function stateOf(v: OffsiteView): CopyState {
  if (!v.enabled) return { kind: 'off' }
  const p = v.pending
  if (!p) return { kind: 'idle' }
  if (p.uploading || !p.error) return { kind: 'copying', p }
  if (p.errorKind === 'host_key_changed') return { kind: 'stopped', p }
  if (p.errorKind && resumable.has(p.errorKind)) return { kind: 'waiting', p }
  return { kind: 'failed', p }
}

/** The page's settings, the last connection test and every action on copies. */
function useCopies(s: ServerStatus, v: OffsiteView, refresh: () => Promise<void>) {
  const ws = useWorkspace()
  const canEdit = can(ws.me, 'backups.copies.manage')
  const readOnly = canEdit ? undefined : t('offsite.holdersOnly')
  const keyReadOnly = can(ws.me, 'backups.recovery_key') ? undefined : t('offsite.key.holdersOnly')
  const canRetry = can(ws.me, 'backups.make')
  const canChangeRules = can(ws.me, 'servers.manage')
  const [edits, setEdits] = useState<Draft>()
  const [test, setTest] = useState<{ result: OffsiteTestResult; at: string }>()
  const [problem, setProblem] = useState<Problem>()
  const [busy, setBusy] = useState<Busy>()
  const [ask, setAsk] = useState<HostKey>()
  const [changed, setChanged] = useState<Changed>()
  const [offer, setOffer] = useState<'first' | 'new'>()
  const [forget, setForget] = useState<Forget>()
  const [madeKey, setMadeKey] = useState<OffsiteView['sshKey']>()
  const draft = edits ?? draftOf(v)
  const dirty = isDirty(draft, v)
  const sshKey = v.sshKey ?? madeKey

  const needKey = canEdit && draft.type === 'sftp' && draft.auth === 'key' && !sshKey
  useEffect(() => {
    if (!needKey) return
    let live = true
    post<NonNullable<OffsiteView['sshKey']>>(serverApi(s.id, '/offsite/ssh-key')).then(
      (k) => {
        if (!live) return
        setMadeKey(k)
        void refresh()
      },
      (e) => live && setProblem(problemOf(e)),
    )
    return () => {
      live = false
    }
  }, [needKey, s.id, refresh])

  function set<K extends keyof Draft>(k: K, value: Draft[K]) {
    setEdits((e) => ({ ...(e ?? draftOf(v)), [k]: value }))
    setTest(undefined)
    setProblem(undefined)
  }

  function discard() {
    setEdits(undefined)
    setTest(undefined)
    setProblem(undefined)
  }

  async function act<T>(what: Busy, fn: () => Promise<T>): Promise<T | undefined> {
    setBusy(what)
    try {
      return await fn()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
      return undefined
    } finally {
      setBusy(undefined)
    }
  }

  /** Tries the settings on the page. toConfirm goes straight to confirming a host key it shows. */
  async function testNow(d: Draft = draft, toConfirm = false): Promise<OffsiteTestResult | undefined> {
    setBusy('test')
    setProblem(undefined)
    setTest(undefined)
    try {
      const result = await post<OffsiteTestResult>(serverApi(s.id, '/offsite/test'), request(d, v))
      const bad = result.checks.find((c) => !c.ok)
      if (result.hostKey && (bad?.kind === 'host_key_unknown' || (toConfirm && bad?.kind === 'host_key_changed'))) setAsk(result.hostKey)
      else if (result.hostKey && bad?.kind === 'host_key_changed') setChanged({ confirmed: bad.params?.pinnedFingerprint ?? v.sftp?.hostKeyFingerprint ?? '', now: result.hostKey.fingerprint, key: result.hostKey })
      else setTest({ result, at: new Date().toISOString() })
      return result
    } catch (e) {
      setProblem(problemOf(e))
      return undefined
    } finally {
      setBusy(undefined)
    }
  }

  /**
   * Saves settings. When they move copies somewhere else while copies are
   * recorded at the old place, the agent refuses until the user agrees to
   * forget them, knowing which backups have no other copy.
   */
  async function postSettings(what: Busy, body: Record<string, unknown>): Promise<OffsiteView | undefined> {
    const url = serverApi(s.id, '/offsite')
    const first = await act(what, async () => {
      try {
        return await post<OffsiteView>(url, body)
      } catch (e) {
        if (!(e instanceof ApiError) || e.reason !== 'copies_recorded') throw e
        const list = await get<{ copies: OffsiteCopy[] }>(serverApi(s.id, '/offsite/copies')).then(
          (r) => r.copies,
          () => [],
        )
        return { refused: e, only: list.filter((c) => !c.onHost) }
      }
    })
    if (!first || !('refused' in first)) return first
    const p = first.refused.params ?? {}
    const ok = await new Promise<boolean>((answer) =>
      setForget({
        place: typeof p.place === 'string' ? p.place : v.place,
        count: typeof p.copies === 'number' ? p.copies : v.copies,
        onlyThere: typeof p.onlyThere === 'number' ? p.onlyThere : first.only.length,
        only: first.only,
        answer,
      }),
    )
    const next = ok ? await act(what, () => post<OffsiteView>(url, { ...body, forgetCopies: true })) : undefined
    setForget(undefined)
    return next
  }

  async function confirmHostKey(k: HostKey) {
    setAsk(undefined)
    const d = { ...draft, hostKey: k.key }
    if (v.enabled && dirty) {
      // Copies still go to the saved place: the key is saved with the rest.
      setEdits(d)
      await testNow(d)
      return
    }
    const next = await postSettings('save', dirty ? request(d, v) : { hostKey: k.key })
    if (!next) return
    setEdits(undefined)
    toastManager.add({ title: t('offsite.hostKey.confirmed'), type: 'success' })
    await refresh()
    await testNow(draftOf(next))
  }

  async function checkNewKey() {
    const k = changed?.key
    setChanged(undefined)
    if (k) setAsk(k)
    else await testNow(draft, true)
  }

  function review(p: OffsitePending) {
    setChanged({ confirmed: p.params?.pinnedFingerprint ?? v.sftp?.hostKeyFingerprint ?? '', now: p.params?.fingerprint ?? '' })
  }

  async function save(enabled?: boolean): Promise<boolean> {
    const body = enabled === undefined ? request(draft, v) : { ...request(draft, v), enabled }
    const next = await postSettings(enabled ? 'on' : 'save', body)
    if (!next) return false
    discard()
    if (enabled && next.key && !next.key.savedAt) setOffer('first')
    else toastManager.add({ title: t(enabled ? 'offsite.onToast' : 'offsite.savedToast'), type: 'success' })
    await refresh()
    return true
  }

  async function turnOff() {
    const next = await act('off', () => post<OffsiteView>(serverApi(s.id, '/offsite'), { enabled: false }))
    if (!next) return
    setTest(undefined)
    toastManager.add({ title: t('offsite.offToast'), type: 'success' })
    await refresh()
  }

  async function testThenTurnOn() {
    const r = await testNow()
    if (r?.ok) await save(true)
  }

  async function retry() {
    if (await act('retry', () => post<OffsiteView>(serverApi(s.id, '/offsite/retry')))) await refresh()
  }

  async function downloadKey() {
    const file = v.key?.fileName
    if (!file) return
    const name = await act('key', () => download(serverApi(s.id, '/offsite/recovery-key'), file))
    if (!name) return
    setOffer(undefined)
    toastManager.add({ title: t('offsite.key.saved', { file: name }), type: 'success' })
    await refresh()
  }

  async function newKey() {
    if (!(await act('newKey', () => post<OffsiteNewKey>(serverApi(s.id, '/offsite/new-key'))))) return
    setOffer('new')
    await refresh()
  }

  return {
    canEdit,
    readOnly,
    keyReadOnly,
    canRetry,
    canChangeRules,
    draft,
    dirty,
    sshKey,
    test,
    problem,
    busy,
    ask,
    changed,
    offer,
    forget,
    set,
    discard,
    testNow,
    confirmHostKey,
    checkNewKey,
    review,
    save,
    turnOff,
    testThenTurnOn,
    retry,
    downloadKey,
    newKey,
    setAsk,
    setChanged,
    setOffer,
  }
}

type Copies = ReturnType<typeof useCopies>

export function Field({ id, label, className, children }: { id: string; label: string; className?: string; children: ReactNode }) {
  return (
    <div className={cn('min-w-0', className)}>
      <label htmlFor={id} className="text-[13px] font-medium">
        {label}
      </label>
      <div className="mt-1.5">{children}</div>
    </div>
  )
}

function CopyIconButton({ text, label, toast }: { text: string; label: string; toast: string }) {
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      className="shrink-0"
      onClick={async () => toastManager.add(await copyText(text) ? { title: toast, type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })}
    >
      <CopyIcon />
    </Button>
  )
}

function DestFields({ c, v, phone }: { c: Copies; v: OffsiteView; phone?: boolean }) {
  const d = c.draft
  const size = phone ? 'lg' : 'default'
  const input = (key: 'endpoint' | 'bucket' | 'accessKeyId' | 'host' | 'port' | 'user' | 'folder', extra: Partial<React.ComponentProps<typeof Input>> = {}) => (
    <Input
      id={`offsite-${key}`}
      value={d[key]}
      onChange={(e) => c.set(key, e.target.value)}
      disabled={!c.canEdit}
      title={c.readOnly}
      aria-invalid={c.problem?.field === key || undefined}
      spellCheck={false}
      autoComplete="off"
      size={size}
      {...extra}
    />
  )
  if (d.type === 's3') {
    return (
      <div className="flex flex-col gap-3">
        <div className={cn('grid gap-3', phone ? 'grid-cols-1' : 'grid-cols-[1fr_minmax(0,0.45fr)]')}>
          <Field id="offsite-endpoint" label={t('offsite.endpoint')}>
            {input('endpoint', { placeholder: t('offsite.endpointPlaceholder'), inputMode: 'url' })}
          </Field>
          <Field id="offsite-bucket" label={t('offsite.bucket')}>
            {input('bucket')}
          </Field>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field id="offsite-accessKeyId" label={t('offsite.keyId')}>
            {input('accessKeyId')}
          </Field>
          <Field id="offsite-secretKey" label={t('offsite.secretKey')}>
            <Input
              id="offsite-secretKey"
              type="password"
              value={d.secretKey}
              onChange={(e) => c.set('secretKey', e.target.value)}
              placeholder={v.s3?.secretKeySet && d.accessKeyId === (v.s3?.accessKeyId ?? '') ? savedDots : undefined}
              aria-description={v.s3?.secretKeySet ? t('offsite.savedSecret') : undefined}
              aria-invalid={c.problem?.field === 'secretKey' || undefined}
              disabled={!c.canEdit}
              title={c.readOnly}
              autoComplete="new-password"
              size={size}
            />
          </Field>
        </div>
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-3">
      <div className={cn('grid gap-3', phone ? 'grid-cols-[1fr_84px]' : 'grid-cols-[1fr_72px]')}>
        <Field id="offsite-host" label={t('offsite.host')}>
          {input('host', { placeholder: t('offsite.hostPlaceholder') })}
        </Field>
        <Field id="offsite-port" label={t('offsite.port')}>
          {input('port', { inputMode: 'numeric', maxLength: 5 })}
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field id="offsite-user" label={t('offsite.user')}>
          {input('user')}
        </Field>
        <Field id="offsite-folder" label={t('offsite.folder')}>
          {input('folder', { placeholder: t('offsite.folderPlaceholder') })}
        </Field>
      </div>
      <div className={cn('flex gap-2', phone ? 'flex-col' : 'items-center justify-between')}>
        <span className="text-[13px] font-medium">{t('offsite.signInWith')}</span>
        <Segmented
          className={phone ? 'grid h-11 grid-cols-2 [&>*]:h-10' : undefined}
          label={t('offsite.signInWith')}
          value={d.auth}
          onChange={(a) => c.set('auth', a)}
          disabledReason={c.readOnly}
          options={[
            { value: 'key', label: t('offsite.authKey') },
            { value: 'password', label: t('offsite.authPassword') },
          ]}
        />
      </div>
      {d.auth === 'key' ? (
        c.sshKey ? (
          <div>
            <div className="flex items-center gap-2 rounded-xl bg-muted py-1.5 ps-3 pe-1.5">
              <code className="min-w-0 flex-1 font-mono text-xs break-all">{shortKeyLine(c.sshKey.authorizedKey)}</code>
              <CopyIconButton text={c.sshKey.authorizedKey} label={t('offsite.copyKeyLine')} toast={t('offsite.keyLineCopied')} />
            </div>
            <p className="mt-1.5 text-xs text-muted-foreground">{d.host.trim() && d.user.trim() ? t('offsite.keyLineHint', { user: d.user.trim(), host: d.host.trim() }) : t('offsite.keyLineHintAny')}</p>
          </div>
        ) : (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <Spinner className="size-3.5" />
            {t('offsite.makingKey')}
          </p>
        )
      ) : (
        <Field id="offsite-password" label={t('offsite.password')}>
          <Input
            id="offsite-password"
            type="password"
            value={d.password}
            onChange={(e) => c.set('password', e.target.value)}
            placeholder={v.sftp?.passwordSet ? savedDots : undefined}
            aria-description={v.sftp?.passwordSet ? t('offsite.savedSecret') : undefined}
            aria-invalid={c.problem?.field === 'password' || undefined}
            disabled={!c.canEdit}
            title={c.readOnly}
            autoComplete="new-password"
            size={size}
          />
        </Field>
      )}
    </div>
  )
}

function CommandBox({ command, phone, className }: { command: string; phone?: boolean; className?: string }) {
  return (
    <div className={cn('flex items-center gap-3 rounded-xl bg-foreground py-2 ps-3.5 pe-2 text-background', className)}>
      <code className="min-w-0 flex-1 font-mono text-xs break-all">{command}</code>
      {phone ? (
        <Button variant="ghost" size="icon-sm" className="shrink-0 text-background hover:bg-background/10" aria-label={t('offsite.copyCommand')} onClick={() => void copyText(command)}>
          <CopyIcon />
        </Button>
      ) : (
        <CopyButton text={command} size="xs" className="shrink-0" />
      )}
    </div>
  )
}

export function ProblemLine({ problem }: { problem: Problem }) {
  return (
    <div role="alert" className="flex items-start gap-2 text-[13px]">
      <CircleXIcon className="mt-0.5 size-4 shrink-0 text-destructive-foreground" aria-hidden="true" />
      <span className="min-w-0">
        <span className="block font-medium">{problem.msg}</span>
        {problem.hint && <span className="block text-xs text-muted-foreground">{problem.hint}</span>}
      </span>
    </div>
  )
}

function TestChecks({ result, type, user }: { result: OffsiteTestResult; type: Dest; user: string }) {
  return (
    <ul className="flex flex-col gap-2">
      {result.checks.map((ch) => (
        <li key={ch.step} className="flex items-start gap-2 text-[13px]">
          {ch.ok ? <CircleCheckIcon className="mt-0.5 size-4 shrink-0 text-success-foreground" aria-hidden="true" /> : <CircleXIcon className="mt-0.5 size-4 shrink-0 text-destructive-foreground" aria-hidden="true" />}
          <span className="min-w-0">
            <span className={cn('block', !ch.ok && 'font-medium')}>{ch.ok ? checkLabel(type, ch, user) : ch.msg}</span>
            {!ch.ok && ch.hint && <span className="block text-xs text-muted-foreground">{ch.hint}</span>}
          </span>
        </li>
      ))}
    </ul>
  )
}

function TestWarning({ result, machine, phone }: { result: OffsiteTestResult; machine: string; phone?: boolean }) {
  if (Math.abs(result.skew) >= clockLimit) {
    const time = formatSpan(Math.abs(result.skew) / 1e9)
    return (
      <div role="status">
        <p className="flex items-center gap-2 text-[13px] font-semibold text-warning-foreground">
          <TriangleAlertIcon className="size-4 shrink-0" aria-hidden="true" />
          {t(result.skew > 0 ? 'offsite.clockBehind' : 'offsite.clockAhead', { machine, time })}
        </p>
        <p className="ms-6 text-xs text-muted-foreground">{t('offsite.clockHint')}</p>
        <CommandBox command={ntpCommand} phone={phone} className="mt-2.5" />
      </div>
    )
  }
  if (!result.warning) return null
  return (
    <p role="status" className="flex items-start gap-2 text-[13px] font-semibold text-warning-foreground">
      <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      {result.warning}
    </p>
  )
}

const testLabel = {
  passed: { desktop: 'offsite.test.passed', phone: 'offsite.test.phonePassed' },
  warning: { desktop: 'offsite.test.warning', phone: 'offsite.test.phoneWarning' },
  failed: { desktop: 'offsite.test.failed', phone: 'offsite.test.phoneFailed' },
} as const satisfies Record<string, Record<'desktop' | 'phone', MessageKey>>

/** Where copies are going now: copying, failed, waiting to carry on, stopped, or the last copy. */
function CopyStatus({ state, v, c, machine, onChangeRules, phone }: { state: CopyState; v: OffsiteView; c: Copies; machine: string; onChangeRules: () => void; phone?: boolean }) {
  switch (state.kind) {
    case 'off':
    case 'idle': {
      const last = v.lastCopy
      if (!last) return <p className="text-xs text-muted-foreground">{t('offsite.noCopyYet')}</p>
      const line = t(v.copies === 1 ? 'offsite.firstCopy' : 'offsite.lastCopy', { when: whenPhrase(last.copiedAt), size: formatBytes(last.sizeBytes) })
      return <p className={cn('font-medium text-success-foreground', phone ? 'text-[15px]' : 'text-xs')}>{last.checked ? `${line} · ${t('offsite.checked')}` : line}</p>
    }
    case 'copying': {
      const p = state.p
      const title = copyingTitle(p)
      const left = p.bytesPerSec && p.total > p.sent ? (p.total - p.sent) / p.bytesPerSec : 0
      return (
        <div>
          <p className={cn('flex items-center gap-2 font-semibold', phone ? 'text-base' : 'text-[13px]')}>
            <Spinner className="size-4 text-info-foreground" />
            {title}
          </p>
          <Progress value={p.total > 0 ? (p.sent / p.total) * 100 : 0} tone="info" className="mt-2.5" label={title} />
          <div className={cn('mt-2 flex justify-between gap-3 text-muted-foreground tabular-nums', phone ? 'text-[13px]' : 'text-xs')}>
            <span>{t('offsite.copying.sent', { sent: formatBytes(p.sent), total: formatBytes(p.total) })}</span>
            {left > 0 && <span>{t('offsite.copying.left', { time: formatSpan(left) })}</span>}
          </div>
          {!phone && <p className="mt-3 text-xs text-muted-foreground">{t('offsite.copying.to', { place: v.place, machine })}</p>}
        </div>
      )
    }
    case 'waiting': {
      const p = state.p
      const reason = {
        network: t('offsite.why.network'),
        rate_limited: t('offsite.why.slow', { place: v.place }),
        service_error: t('offsite.why.service', { place: v.place }),
      }[p.errorKind ?? ''] ?? t('offsite.why.interrupted')
      const pct = p.total > 0 ? (p.sent / p.total) * 100 : 0
      const soon = !p.nextAttempt || new Date(p.nextAttempt).getTime() <= Date.now()
      const when = p.nextAttempt && daysAgo(p.nextAttempt) === 0 ? t('offsite.at', { time: formatClock(p.nextAttempt) }) : t('offsite.onDay', { day: p.nextAttempt ? whenPhrase(p.nextAttempt) : '' })
      const title = p.sent > 0 ? t('offsite.stoppedAt', { percent: formatPercent(Math.floor(pct)), reason }) : t('offsite.notYet', { reason })
      const sub = p.sent > 0 ? (soon ? t('offsite.carriesOnSoon', { sent: formatBytes(p.sent) }) : t('offsite.carriesOn', { sent: formatBytes(p.sent), when })) : soon ? t('offsite.triesSoon') : t('offsite.triesAgain', { when })
      return (
        <div>
          <p className={cn('flex items-center gap-2 font-semibold', phone ? 'text-base' : 'text-[13px]')}>
            <HourglassIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            {title}
          </p>
          {p.sent > 0 && <Progress value={pct} tone="muted" className="mt-2.5" label={title} />}
          <p className={cn('mt-2 text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{sub}</p>
          {c.canRetry && (
            <Button size="sm" variant="outline" className="mt-3" loading={c.busy === 'retry'} onClick={() => void c.retry()}>
              <RefreshCwIcon />
              {t('offsite.tryNow')}
            </Button>
          )}
        </div>
      )
    }
    case 'failed': {
      const p = state.p
      const full = p.errorKind === 'storage_full'
      return (
        <div role="alert">
          <p className={cn('flex items-center gap-2 font-semibold', phone ? 'text-base' : 'text-[13px]')}>
            <CircleXIcon className="size-4 shrink-0 text-destructive-foreground" aria-hidden="true" />
            {full ? t('offsite.full', { place: v.place }) : p.error}
          </p>
          {(full || p.hint) && <p className={cn('mt-1 ms-6 text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{full ? t('offsite.fullHint') : p.hint}</p>}
          {(c.canRetry || c.canChangeRules) && (
            <div className="mt-3 flex items-center gap-2">
              {c.canRetry && (
                <Button size="sm" variant="outline" loading={c.busy === 'retry'} onClick={() => void c.retry()}>
                  <RefreshCwIcon />
                  {t('offsite.tryAgain')}
                </Button>
              )}
              {c.canChangeRules && (
                <Button size="sm" variant="ghost" onClick={onChangeRules}>
                  {t('backupRules.change')}
                </Button>
              )}
            </div>
          )}
        </div>
      )
    }
    case 'stopped': {
      const host = v.sftp?.host ?? v.place
      if (phone) {
        return (
          <button type="button" className="flex w-full items-center gap-3 text-left" onClick={() => c.review(state.p)}>
            <CircleXIcon className="size-5 shrink-0 text-destructive-foreground" aria-hidden="true" />
            <span className="min-w-0 flex-1">
              <span className="block text-base font-semibold text-destructive-foreground">{t('offsite.stoppedTitle')}</span>
              <span className="block text-[13px] text-muted-foreground">{t('offsite.keyChanged', { host })}</span>
            </span>
            <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
          </button>
        )
      }
      return (
        <div role="alert" className="flex items-center justify-between gap-3">
          <p className="flex min-w-0 items-center gap-2 text-[13px] font-medium text-destructive-foreground">
            <CircleXIcon className="size-4 shrink-0" aria-hidden="true" />
            {t('offsite.stoppedKey', { host })}
          </p>
          <Button size="sm" variant="outline" onClick={() => c.review(state.p)}>
            {t('offsite.review')}
          </Button>
        </div>
      )
    }
    default: {
      const unknown: never = state
      return unknown
    }
  }
}

function KeyRow({ v, c, machine }: { v: OffsiteView; c: Copies; machine: string }) {
  const k = v.key
  if (!k) return null
  if (!k.savedAt) {
    const renewed = k.oldKeys > 0
    return (
      <div role="status" className="flex items-center gap-3">
        <KeyRoundIcon className="size-4 shrink-0 text-warning-foreground" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-semibold text-warning-foreground">{t(renewed ? 'offsite.key.newNotDownloaded' : 'offsite.key.notDownloaded')}</p>
          <p className="text-xs text-muted-foreground">{renewed ? t('offsite.key.newNotDownloadedHint') : t('offsite.key.notDownloadedHint', { machine })}</p>
        </div>
        <Button size="sm" variant="outline" loading={c.busy === 'key'} disabledReason={c.keyReadOnly} onClick={() => void c.downloadKey()}>
          <DownloadIcon />
          {t('common.download')}
        </Button>
      </div>
    )
  }
  return (
    <div className="flex items-center gap-3">
      <KeyRoundIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-[13px] font-semibold">{t('offsite.key.row')}</p>
        <p className="text-xs text-muted-foreground">{t('offsite.key.downloadedAt', { date: formatDate(k.savedAt) })}</p>
      </div>
      <Button size="sm" variant="outline" loading={c.busy === 'key'} disabledReason={c.keyReadOnly} onClick={() => void c.downloadKey()}>
        <DownloadIcon />
        {t('offsite.key.downloadAgain')}
      </Button>
      <Menu>
        <MenuTrigger render={<Button variant="outline" size="icon-sm" aria-label={t('offsite.key.menu')} disabledReason={c.keyReadOnly} />}>
          <EllipsisIcon />
        </MenuTrigger>
        <MenuPopup align="end" className="min-w-52">
          <MenuItem onClick={() => void c.newKey()}>
            <RefreshCwIcon />
            {t('offsite.key.makeNew')}
          </MenuItem>
          <MenuLinkItem href={t('offsite.key.restoringUrl')} target="_blank" rel="noreferrer">
            <BookOpenIcon />
            {t('offsite.key.howRestoring')}
            <ExternalLinkIcon className="ms-auto opacity-60" />
          </MenuLinkItem>
        </MenuPopup>
      </Menu>
    </div>
  )
}

function FingerprintBox({ rows }: { rows: { label: string; value: string; red?: boolean }[] }) {
  return (
    <div className="flex flex-col gap-2.5 rounded-xl bg-muted px-3.5 py-3">
      {rows.map((r) => (
        <div key={r.label}>
          <p className="text-xs text-muted-foreground">{r.label}</p>
          <p className={cn('mt-0.5 font-mono text-[13px] font-semibold break-all', r.red && 'text-destructive-foreground')}>{r.value}</p>
        </div>
      ))}
    </div>
  )
}

export function HostKeyDialog({ host, hostKey, phone, busy, onConfirm, onClose }: { host: string; hostKey: HostKey; phone: boolean; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const kind = hostKeyKind(hostKey.type)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={phone}>
        <DialogHeader>
          <DialogTitle>{t('offsite.hostKey.title', { host })}</DialogTitle>
          <DialogDescription>{t('offsite.hostKey.body')}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-3">
          <FingerprintBox rows={[{ label: t('offsite.hostKey.showed', { type: kind.label }), value: hostKey.fingerprint }]} />
          <p className="text-[13px]">{t('offsite.hostKey.check', { host })}</p>
          <CommandBox command={`ssh-keygen -lf /etc/ssh/ssh_host_${kind.file}_key.pub`} phone={phone} />
        </DialogPanel>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4 max-sm:border-t-0">
          <Button variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button size={phone ? 'touch' : 'default'} loading={busy} onClick={onConfirm}>
            <ShieldCheckIcon />
            {t('offsite.hostKey.confirm')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function HostKeyChangedDialog({ host, changed, phone, busy, onCheck, onClose }: { host: string; changed: Changed; phone: boolean; busy: boolean; onCheck: () => void; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={phone}>
        <DialogHeader>
          <DialogTitle>{t('offsite.hostKey.changedTitle', { host })}</DialogTitle>
          <DialogDescription>{t('offsite.hostKey.changedBody')}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-3">
          <FingerprintBox
            rows={[
              { label: t('offsite.hostKey.youConfirmed'), value: changed.confirmed },
              ...(changed.now ? [{ label: t('offsite.hostKey.showsNow'), value: changed.now, red: true }] : []),
            ]}
          />
          <p className="text-[13px] text-muted-foreground">{t('offsite.hostKey.why')}</p>
        </DialogPanel>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4 max-sm:border-t-0">
          <Button variant={phone ? 'ghost' : 'outline'} size={phone ? 'touch' : 'default'} onClick={onClose}>
            {t('common.close')}
          </Button>
          <Button size={phone ? 'touch' : 'default'} loading={busy} onClick={onCheck}>
            {t('offsite.hostKey.checkNew')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function RecoveryKeyDialog({ kind, server, machine, fileName, phone, busy, onDownload, onClose }: { kind: 'first' | 'new'; server: string; machine: string; fileName: string; phone: boolean; busy: boolean; onDownload: () => void; onClose: () => void }) {
  const first = kind === 'first'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[500px]" showCloseButton={false}>
        <DialogHeader className={cn('gap-1', !phone && 'ps-14')}>
          <KeyRoundIcon className={cn('size-5 text-warning-foreground', phone ? 'mb-3' : 'absolute top-7 left-6')} aria-hidden="true" />
          <DialogTitle>{first ? t('offsite.key.offerTitle', { server }) : t('offsite.key.newTitle')}</DialogTitle>
          <DialogDescription>{first ? t('offsite.key.offerBody', { machine }) : t('offsite.key.newBody')}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-3">
          <div className="flex items-center gap-3 rounded-xl border border-border px-3.5 py-2.5">
            <FileDownIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <div className="min-w-0">
              <p className="truncate text-[13px] font-semibold">{fileName}</p>
              <p className="text-xs text-muted-foreground">{first ? t('offsite.key.fileHint', { server }) : t('offsite.key.newFileHint')}</p>
            </div>
          </div>
          {first ? (
            <p className="text-[13px] text-muted-foreground">{t('offsite.key.where', { machine })}</p>
          ) : (
            <div className="flex flex-col gap-1.5 text-[13px] text-muted-foreground">
              <p>{t('offsite.key.newWhy')}</p>
              {!phone && <p>{t('offsite.key.newLeaked')}</p>}
            </div>
          )}
        </DialogPanel>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4 sm:items-center max-sm:border-t-0">
          {first && !phone && (
            <a href={t('offsite.key.restoringUrl')} target="_blank" rel="noreferrer" className="me-auto inline-flex items-center gap-1 rounded text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
              {t('offsite.key.learnMore')}
              <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
            </a>
          )}
          <Button variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
            {t('common.later')}
          </Button>
          <Button size={phone ? 'touch' : 'default'} loading={busy} onClick={onDownload}>
            <DownloadIcon />
            {first ? t('common.download') : t('offsite.key.downloadNew')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

const forgetListed = 5

/** Before copies go somewhere else: the copies at the old place stay there, but aren't listed here any more. */
function ForgetCopiesDialog({ forget: f, phone, busy }: { forget: Forget; phone: boolean; busy: boolean }) {
  const shown = f.only.slice(0, forgetListed)
  return (
    <Dialog open onOpenChange={(o) => !o && f.answer(false)}>
      <DialogPopup className="sm:max-w-[500px]" showCloseButton={phone}>
        <DialogHeader>
          <DialogTitle>{t('offsite.forget.title', { place: f.place })}</DialogTitle>
          <DialogDescription>{t('offsite.forget.body', { count: f.count, place: f.place })}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-3">
          {f.onlyThere > 0 && (
            <div role="alert" className="rounded-xl bg-muted px-3.5 py-3">
              <p className="flex items-center gap-2 text-[13px] font-semibold text-warning-foreground">
                <TriangleAlertIcon className="size-4 shrink-0" aria-hidden="true" />
                {t('offsite.forget.only', { count: f.onlyThere })}
              </p>
              {shown.length > 0 && (
                <ul className="mt-2 ms-6 flex flex-col gap-0.5 text-[13px] tabular-nums">
                  {shown.map((c) => (
                    <li key={c.backupId}>{t('offsite.forget.backup', { when: whenPhrase(c.createdAt), size: formatBytes(c.sizeBytes) })}</li>
                  ))}
                  {f.only.length > shown.length && <li className="text-muted-foreground">{t('offsite.forget.more', { count: f.only.length - shown.length })}</li>}
                </ul>
              )}
            </div>
          )}
          <p className="text-[13px] text-muted-foreground">{t('offsite.forget.back', { place: f.place })}</p>
        </DialogPanel>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4 max-sm:border-t-0">
          <Button variant="ghost" size={phone ? 'touch' : 'default'} onClick={() => f.answer(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant={f.onlyThere > 0 ? 'destructive' : 'default'} size={phone ? 'touch' : 'default'} loading={busy} onClick={() => f.answer(true)}>
            {t('offsite.forget.confirm')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function CopiesDialogs({ c, v, server, machine, phone }: { c: Copies; v: OffsiteView; server: ServerStatus; machine: string; phone: boolean }) {
  const host = c.draft.host.trim() || v.sftp?.host || ''
  return (
    <>
      {c.forget && <ForgetCopiesDialog forget={c.forget} phone={phone} busy={c.busy === 'save' || c.busy === 'on'} />}
      {c.ask && <HostKeyDialog host={host} hostKey={c.ask} phone={phone} busy={c.busy === 'save' || c.busy === 'test'} onConfirm={() => c.ask && void c.confirmHostKey(c.ask)} onClose={() => c.setAsk(undefined)} />}
      {c.changed && <HostKeyChangedDialog host={host} changed={c.changed} phone={phone} busy={c.busy === 'test'} onCheck={() => void c.checkNewKey()} onClose={() => c.setChanged(undefined)} />}
      {c.offer && v.key && <RecoveryKeyDialog kind={c.offer} server={server.name} machine={machine} fileName={v.key.fileName} phone={phone} busy={c.busy === 'key'} onDownload={() => void c.downloadKey()} onClose={() => c.setOffer(undefined)} />}
    </>
  )
}

function StateMarker({ v }: { v: OffsiteView }) {
  const state = stateOf(v)
  if (state.kind === 'off') return <Marker>{t('offsite.off')}</Marker>
  if (state.kind === 'stopped') return <Marker tone="red">{t('offsite.stopped')}</Marker>
  return <Marker tone="green">{t('offsite.on')}</Marker>
}

/** Backup rules › Copies somewhere else: where copies go, the connection test, what they're doing and the recovery key. */
export function CopiesCard({ server: s, onChangeRules }: { server: ServerStatus; onChangeRules: () => void }) {
  const off = useOffsite(s.id)
  if (off.data) return <DesktopCopies server={s} view={off.data} refresh={off.refresh} onChangeRules={onChangeRules} />
  if (off.error) {
    return (
      <Card>
        <p className="text-sm text-destructive-foreground">{off.error.message}</p>
      </Card>
    )
  }
  return (
    <>
      <LoadingLabel />
      <Skeleton className="h-[440px] rounded-3xl" />
    </>
  )
}

function DesktopCopies({ server: s, view: v, refresh, onChangeRules }: { server: ServerStatus; view: OffsiteView; refresh: () => Promise<void>; onChangeRules: () => void }) {
  const ws = useWorkspace()
  const c = useCopies(s, v, refresh)
  const d = c.draft
  const r = c.test?.result
  const tone = r && testTone(r)
  const state = stateOf(v)
  const editing = !v.enabled || c.dirty
  // While copies are stopped the footer holds Review, so the test sits beside the recovery key.
  const testWhileStopped = state.kind === 'stopped' && !editing && !r
  const testButton = (primary: boolean) => (
    <Button size="sm" variant={primary ? 'default' : 'outline'} loading={c.busy === 'test'} disabledReason={c.readOnly} onClick={() => void c.testNow()}>
      {t('offsite.test')}
    </Button>
  )
  let footer: ReactNode
  if (r) {
    const action = !r.ok ? (
      <Button size="sm" variant="outline" loading={c.busy === 'test'} onClick={() => void c.testNow()}>
        <RefreshCwIcon />
        {t('offsite.testAgain')}
      </Button>
    ) : !v.enabled ? (
      <Button size="sm" loading={c.busy === 'on'} onClick={() => void c.save(true)}>
        {t('offsite.turnOn')}
      </Button>
    ) : c.dirty ? (
      <Button size="sm" loading={c.busy === 'save'} onClick={() => void c.save()}>
        {t('offsite.saveChanges')}
      </Button>
    ) : (
      <Button size="sm" variant="outline" loading={c.busy === 'test'} onClick={() => void c.testNow()}>
        <RefreshCwIcon />
        {t('offsite.testAgain')}
      </Button>
    )
    footer = (
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted-foreground">{r.ok && c.test ? t('offsite.tested', { time: relativeTime(c.test.at) }) : t('offsite.nothingCopied')}</p>
        {action}
      </div>
    )
  } else if (editing) {
    footer = (
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted-foreground">{t(v.enabled ? 'offsite.testToSave' : 'offsite.encryptedStart')}</p>
        {testButton(true)}
      </div>
    )
  } else if (state.kind === 'idle') {
    footer = (
      <div className="flex items-center justify-between gap-3">
        <CopyStatus state={state} v={v} c={c} machine={ws.machineName} onChangeRules={onChangeRules} />
        {testButton(false)}
      </div>
    )
  } else {
    footer = <CopyStatus state={state} v={v} c={c} machine={ws.machineName} onChangeRules={onChangeRules} />
  }
  return (
    <Card className="flex flex-col">
      <div className="flex items-center gap-2">
        <CardTitle>{t('offsite.title')}</CardTitle>
        <StateMarker v={v} />
        {v.enabled && c.canEdit && (
          <Menu>
            <MenuTrigger render={<Button variant="ghost" size="icon-sm" className="ms-auto -my-1" aria-label={t('offsite.more')} />}>
              <EllipsisIcon />
            </MenuTrigger>
            <MenuPopup align="end">
              <MenuItem onClick={() => void c.turnOff()}>
                <PowerOffIcon />
                {t('offsite.turnOff')}
              </MenuItem>
            </MenuPopup>
          </Menu>
        )}
      </div>
      <p className="mt-4 mb-1.5 text-[13px] font-medium">{t('offsite.whereTo')}</p>
      <CardGroup value={d.type} onChange={(x) => c.canEdit && c.set('type', x)} label={t('offsite.whereTo')} className="flex flex-col gap-2">
        {(['s3', 'sftp'] as const).map((x) => (
          <ChoiceCard key={x} value={x} radio="start" disabled={!c.canEdit} reason={c.readOnly} className="gap-3 px-3.5 py-2.5">
            <span className="block text-[13px] font-semibold">{t(x === 's3' ? 'offsite.s3' : 'offsite.sftp')}</span>
            <span className="block text-xs text-muted-foreground">{t(x === 's3' ? 'offsite.s3Hint' : 'offsite.sftpHint')}</span>
          </ChoiceCard>
        ))}
      </CardGroup>
      <div className="mt-4">
        <DestFields c={c} v={v} />
      </div>
      {!c.canEdit && <p className="mt-3 text-xs text-muted-foreground">{t('offsite.holdersOnly')}</p>}
      {v.enabled && <p className="mt-3 text-xs text-muted-foreground">{t('offsite.encryptedKeep')}</p>}
      {(v.key || testWhileStopped) && (
        <div className="mt-3 flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
          {v.key && (
            <div className="min-w-64 flex-1">
              <KeyRow v={v} c={c} machine={ws.machineName} />
            </div>
          )}
          {testWhileStopped && testButton(false)}
        </div>
      )}
      {r && tone && (
        <div className="mt-4 border-t border-border pt-4" aria-live="polite">
          <SectionLabel>{t(testLabel[tone].desktop)}</SectionLabel>
          <p className="mt-1.5 mb-2.5 text-[13px] font-semibold">{d.type === 's3' ? `${placeOf(d, v)} · ${d.bucket.trim()}` : placeOf(d, v)}</p>
          <TestChecks result={r} type={d.type} user={d.user.trim()} />
          <div className="mt-3 empty:hidden">
            <TestWarning result={r} machine={ws.machineName} />
          </div>
        </div>
      )}
      {c.problem && (
        <div className="mt-4">
          <ProblemLine problem={c.problem} />
        </div>
      )}
      <div className="mt-4 border-t border-border pt-4">{footer}</div>
      <CopiesDialogs c={c} v={v} server={s} machine={ws.machineName} phone={false} />
    </Card>
  )
}

/** Phone: Backup rules › Copies. */
export function CopiesPhonePage({ server: s }: { server: ServerStatus }) {
  const off = useOffsite(s.id)
  return (
    <>
      <PhoneBackHeader to={{ name: 'server', slug: s.slug, tab: 'world', sub: 'backup-rules' }} label={t('backupRules.title')} title={t('offsite.phoneTitle')} />
      {off.data ? (
        <PhoneCopies server={s} view={off.data} refresh={off.refresh} />
      ) : off.error ? (
        <p className="px-4 text-[15px] text-destructive-foreground">{off.error.message}</p>
      ) : (
        <>
          <LoadingLabel />
          <Skeleton className="h-80 rounded-3xl" />
        </>
      )}
    </>
  )
}

function PhoneCopies({ server: s, view: v, refresh }: { server: ServerStatus; view: OffsiteView; refresh: () => Promise<void> }) {
  const ws = useWorkspace()
  const c = useCopies(s, v, refresh)
  const [editing, setEditing] = useState(false)
  const d = c.draft
  const r = c.test?.result
  const tone = r && testTone(r)
  const form = editing || !v.configured
  const state = stateOf(v)
  const changeRules = () => navigate({ name: 'server', slug: s.slug, tab: 'world', sub: 'backup-rules' })
  const done = (ok: boolean) => ok && setEditing(false)
  const switchLocked = c.readOnly ?? (c.busy === 'test' ? t('offsite.testing') : c.busy === 'on' || c.busy === 'off' || c.busy === 'save' ? t('reason.saving') : undefined)

  let primary: ReactNode
  let secondary: ReactNode
  const testAgain = (
    <Button size="touch" variant="ghost" loading={c.busy === 'test'} onClick={() => void c.testNow()}>
      {t('offsite.testAgain')}
    </Button>
  )
  if (r?.ok && !v.enabled) {
    primary = (
      <Button size="touch" loading={c.busy === 'on'} onClick={async () => done(await c.save(true))}>
        {t('offsite.turnOn')}
      </Button>
    )
    secondary = testAgain
  } else if (r?.ok && c.dirty) {
    primary = (
      <Button size="touch" loading={c.busy === 'save'} onClick={async () => done(await c.save())}>
        {t('offsite.saveChanges')}
      </Button>
    )
    secondary = testAgain
  } else if (r) {
    primary = (
      <Button size="touch" variant="outline" loading={c.busy === 'test'} onClick={() => void c.testNow()}>
        {t('offsite.testAgain')}
      </Button>
    )
  } else {
    primary = (
      <Button size="touch" variant={form ? 'default' : 'outline'} loading={c.busy === 'test'} disabledReason={c.readOnly} onClick={() => void c.testNow()}>
        {t('offsite.test')}
      </Button>
    )
    if (editing && v.configured) {
      secondary = (
        <Button
          size="touch"
          variant="ghost"
          onClick={() => {
            c.discard()
            setEditing(false)
          }}
        >
          {t('common.cancel')}
        </Button>
      )
    }
  }

  const k = v.key
  return (
    <div className="flex flex-col gap-2 pb-40">
      {v.configured && !form && (
        <div className="flex min-h-[60px] items-center justify-between gap-3 rounded-3xl border border-border bg-white px-4 py-2">
          <span className="min-w-0">
            <span className="block text-base">{t('offsite.switch')}</span>
            <span className="block text-[13px] text-muted-foreground">{t('offsite.switchHint')}</span>
          </span>
          <Switch checked={v.enabled} disabled={!!switchLocked} title={switchLocked} onCheckedChange={(on) => void (on ? c.testThenTurnOn() : c.turnOff())} aria-label={t('offsite.switch')} />
        </div>
      )}
      <SectionLabel className="mt-2 px-4">{t('offsite.whereTo')}</SectionLabel>
      {form ? (
        <div className="flex flex-col gap-3">
          <ChoiceSelect
            className="min-h-14 w-full justify-between rounded-3xl border border-border bg-white px-4"
            label={t('offsite.whereTo')}
            value={d.type}
            onChange={(x) => c.set('type', x)}
            disabledReason={c.readOnly}
            options={[
              { value: 's3', label: t('offsite.s3') },
              { value: 'sftp', label: t('offsite.sftp') },
            ]}
          />
          <DestFields c={c} v={v} phone />
        </div>
      ) : (
        <button type="button" disabled={!c.canEdit} title={c.readOnly} className="flex min-h-16 items-center gap-3 rounded-3xl border border-border bg-white px-4 py-2 text-left" onClick={() => setEditing(true)}>
          {v.type === 'sftp' ? <ServerIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" /> : <HardDriveIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />}
          <span className="min-w-0 flex-1">
            <span className="block truncate text-base font-medium">{v.place}</span>
            <span className="block truncate text-[13px] text-muted-foreground">
              {v.type === 'sftp' ? t('offsite.sftpLine', { user: v.sftp?.user ?? '', folder: v.sftp?.folder ?? '' }) : t('offsite.bucketLine', { bucket: v.s3?.bucket ?? '' })}
            </span>
          </span>
          <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
        </button>
      )}
      {!c.canEdit && <p className="px-4 text-[13px] text-muted-foreground">{t('offsite.holdersOnly')}</p>}
      {c.problem && (
        <div className="px-1 pt-1">
          <ProblemLine problem={c.problem} />
        </div>
      )}
      {r && tone ? (
        <>
          <SectionLabel className="mt-2 px-4">{t(testLabel[tone].phone)}</SectionLabel>
          <div className="rounded-3xl border border-border bg-white p-4">
            <TestChecks result={r} type={d.type} user={d.user.trim()} />
          </div>
          <div className="px-1 pt-2 empty:hidden">
            <TestWarning result={r} machine={ws.machineName} phone />
          </div>
        </>
      ) : (
        v.enabled &&
        !form && (
          <>
            <SectionLabel className="mt-2 px-4">{t('offsite.lastCopyLabel')}</SectionLabel>
            <div className="rounded-3xl border border-border bg-white p-4">
              <CopyStatus state={state} v={v} c={c} machine={ws.machineName} onChangeRules={changeRules} phone />
            </div>
          </>
        )
      )}
      {k && !form && (
        <>
          <SectionLabel className="mt-2 px-4">{t('offsite.key.row')}</SectionLabel>
          <div className="rounded-3xl border border-border bg-white px-4">
            <div className="flex flex-col gap-3 border-b border-border py-4">
              {k.savedAt ? (
                <div className="flex items-center gap-3">
                  <KeyRoundIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="min-w-0 flex-1">
                    <span className="block text-base font-medium">{t('offsite.key.row')}</span>
                    <span className="block text-[13px] text-muted-foreground">{t('offsite.key.downloadedAt', { date: formatDate(k.savedAt) })}</span>
                  </span>
                </div>
              ) : (
                <div role="status" className="flex items-start gap-3">
                  <KeyRoundIcon className="mt-0.5 size-5 shrink-0 text-warning-foreground" aria-hidden="true" />
                  <span className="min-w-0">
                    <span className="block text-base font-semibold text-warning-foreground">{t(k.oldKeys > 0 ? 'offsite.key.newNotDownloaded' : 'offsite.key.notDownloaded')}</span>
                    <span className="block text-[13px] text-muted-foreground">{k.oldKeys > 0 ? t('offsite.key.newNotDownloadedHint') : t('offsite.key.notDownloadedHint', { machine: ws.machineName })}</span>
                  </span>
                </div>
              )}
              <Button size="touch" variant="outline" loading={c.busy === 'key'} disabledReason={c.keyReadOnly} onClick={() => void c.downloadKey()}>
                <DownloadIcon />
                {k.savedAt ? t('offsite.key.downloadAgain') : t('offsite.key.download')}
              </Button>
            </div>
            <button type="button" disabled={!!c.keyReadOnly || c.busy === 'newKey'} title={c.keyReadOnly ?? (c.busy === 'newKey' ? t('offsite.key.making') : undefined)} aria-busy={c.busy === 'newKey' || undefined} className="flex min-h-14 w-full items-center gap-3 text-left" onClick={() => void c.newKey()}>
              {c.busy === 'newKey' ? <Spinner className="size-5" /> : <RefreshCwIcon className="size-5 text-muted-foreground" aria-hidden="true" />}
              <span className="flex-1 text-base">{t('offsite.key.makeNew')}</span>
              <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
            </button>
          </div>
        </>
      )}
      <div className="fixed inset-x-4 bottom-[calc(76px+env(safe-area-inset-bottom))] z-20 flex flex-col gap-1">
        {primary}
        {secondary}
      </div>
      <CopiesDialogs c={c} v={v} server={s} machine={ws.machineName} phone />
    </div>
  )
}

import { useEffect, useRef, useState, type DragEvent, type ReactNode } from 'react'
import { ArrowRightIcon, CheckIcon, ChevronDownIcon, ChevronUpIcon, CircleCheckIcon, HardDriveIcon, KeyRoundIcon, UploadIcon, XIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { Operation, RecoverView, RestorePreview } from '@/api/types'
import { errorText, machineApi, useWorkspace, type Workspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Card, Notice, SectionLabel } from '@/components/app/bits'
import { CardGroup, ChoiceCard, ChoiceSelect, useIsPhone } from '@/components/app/controls'
import { PhoneActions } from '@/components/app/frame'
import { RestoreDialog } from '@/components/app/restore'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatBytes, formatDate, formatDay } from '@/lib/format'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { Field, HostKeyDialog, ProblemLine, type HostKey, type Problem } from './server/copies'
import { CopyJobDialog } from './server/copy-restore'

const newestShown = 4
const maxKeyFile = 64 * 1024

interface KeyFile {
  name: string
  text: string
  server: string
  keys: number
  made?: string
}

interface Where {
  type: 's3' | 'sftp'
  endpoint: string
  bucket: string
  accessKeyId: string
  secretKey: string
  host: string
  port: string
  user: string
  folder: string
  password: string
}

const emptyWhere: Where = { type: 's3', endpoint: '', bucket: '', accessKeyId: '', secretKey: '', host: '', port: '22', user: '', folder: '', password: '' }

/** What the page shows about a key file; the agent does the real checking. */
function readKeyFile(name: string, text: string): KeyFile | undefined {
  const lines = text.split('\n').map((l) => l.trim())
  const keys = lines.filter((l) => l.startsWith('AGE-SECRET-KEY-1')).length
  if (keys === 0) return undefined
  const title = '# Playkeeper recovery key for '
  const server = lines.find((l) => l.startsWith(title))?.slice(title.length) ?? ''
  const made = /^# Made (\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}) UTC/.exec(lines.find((l) => l.startsWith('# Made ')) ?? '')
  return { name, text, server, keys, made: made ? `${made[1]}T${made[2]}:00Z` : undefined }
}

function ready(w: Where): boolean {
  return w.type === 's3' ? !!(w.endpoint.trim() && w.bucket.trim() && w.accessKeyId.trim() && w.secretKey) : !!(w.host.trim() && w.user.trim() && w.password)
}

function requestOf(k: KeyFile, w: Where, hostKey: string, name?: string): Record<string, unknown> {
  const body: Record<string, unknown> =
    w.type === 's3'
      ? { recoveryKey: k.text, config: { type: 's3', s3: { endpoint: w.endpoint.trim(), bucket: w.bucket.trim(), accessKeyId: w.accessKeyId.trim() } }, secretKey: w.secretKey }
      : { recoveryKey: k.text, config: { type: 'sftp', sftp: { host: w.host.trim(), port: Number(w.port) || 22, user: w.user.trim(), folder: w.folder.trim() } }, password: w.password }
  if (hostKey && w.type === 'sftp') body.hostKey = hostKey
  if (name) body.name = name
  return body
}

function problemOf(e: unknown): Problem {
  return e instanceof ApiError ? { msg: e.message, hint: e.hint, field: e.field } : { msg: errorText(e) }
}

function keysText(k: KeyFile): string {
  return [t('recover.keys', { count: k.keys }), k.made && t('recover.made', { date: formatDate(k.made) })].filter(Boolean).join(t('common.dot'))
}

function useRecover() {
  const ws = useWorkspace()
  const mid = ws.machine?.id
  const [key, setKey] = useState<KeyFile>()
  const [where, setWhere] = useState<Where>(emptyWhere)
  const [hostKey, setHostKey] = useState('')
  const [ask, setAsk] = useState<HostKey>()
  const [view, setView] = useState<RecoverView>()
  const [picked, setPicked] = useState<string>()
  const [problem, setProblem] = useState<Problem>()
  const [busy, setBusy] = useState(false)
  const [started, setStarted] = useState<Operation>()
  const [hidden, setHidden] = useState<string>()
  const [loadError, setLoadError] = useState<string>()
  const [preview, setPreview] = useState<RestorePreview>()
  const fetched = useRef<string | undefined>(undefined)

  const job = usePoll(() => (started && mid ? get<Operation>(machineApi(mid, `/operations/${started.id}`)) : Promise.resolve(undefined)), 1000, started?.id ?? '')
  const op = started && (job.data?.id === started.id ? job.data : started)
  const jobOpen = !!op && hidden !== `${op.id}:${op.status}`
  const restoreId = op?.status === 'succeeded' && typeof op.detail?.restoreId === 'string' ? op.detail.restoreId : undefined

  useEffect(() => {
    if (!jobOpen || !restoreId || !mid || fetched.current === restoreId) return
    fetched.current = restoreId
    void (async () => {
      try {
        const p = await get<RestorePreview>(machineApi(mid, `/restore/${restoreId}`))
        setStarted(undefined)
        setPreview(p)
      } catch (e) {
        setLoadError(errorText(e))
      }
    })()
  }, [jobOpen, restoreId, mid])

  async function chooseFile(f: File) {
    setView(undefined)
    setProblem(undefined)
    const k = f.size <= maxKeyFile ? readKeyFile(f.name, await f.text()) : undefined
    setKey(k)
    if (!k) setProblem({ msg: t('recover.notKeyFile'), hint: t('recover.notKeyFileHint'), field: 'recoveryKey' })
  }

  function set<K extends keyof Where>(k: K, v: Where[K]) {
    setWhere((w) => ({ ...w, [k]: v }))
    setView(undefined)
    setProblem(undefined)
    if (k === 'host' || k === 'port' || k === 'type') setHostKey('')
  }

  async function connect(confirmed = hostKey) {
    if (!key || !mid) return
    setBusy(true)
    setProblem(undefined)
    try {
      const v = await post<RecoverView>(machineApi(mid, '/offsite/recover'), requestOf(key, where, confirmed))
      setView(v)
      setPicked(v.copies[0]?.name)
    } catch (e) {
      const p = e instanceof ApiError ? e.params : undefined
      if (e instanceof ApiError && e.reason === 'host_key_unknown' && typeof p?.hostKey === 'string') {
        setAsk({ key: p.hostKey, type: String(p.keyType ?? ''), fingerprint: String(p.fingerprint ?? '') })
      } else {
        setProblem(problemOf(e))
      }
    } finally {
      setBusy(false)
    }
  }

  function confirmHostKey() {
    if (!ask) return
    setHostKey(ask.key)
    setAsk(undefined)
    void connect(ask.key)
  }

  async function start() {
    if (op && op.status === 'running') {
      setHidden(undefined)
      return
    }
    if (!key || !mid || !picked) return
    setBusy(true)
    try {
      const o = await post<Operation>(machineApi(mid, '/offsite/recover/restore'), requestOf(key, where, hostKey, picked))
      setStarted(o)
      setHidden(undefined)
      setLoadError(undefined)
    } catch (e) {
      setProblem(problemOf(e))
    } finally {
      setBusy(false)
    }
  }

  const copy = view?.copies.find((c) => c.name === picked)
  return {
    key,
    where,
    view,
    picked,
    copy,
    problem,
    busy,
    ask,
    op,
    jobOpen,
    loadError,
    preview,
    chooseFile,
    set,
    connect,
    confirmHostKey,
    start,
    setPicked,
    setAsk,
    hideJob: () => op && setHidden(`${op.id}:${op.status}`),
    closePreview: () => setPreview(undefined),
  }
}

type Recover = ReturnType<typeof useRecover>

function StepHead({ n, done, title }: { n: number; done: boolean; title: string }) {
  return (
    <h2 className="flex items-center gap-2.5 text-sm font-semibold">
      {done ? (
        <span className="flex size-5 items-center justify-center rounded-full bg-primary text-primary-foreground" aria-hidden="true">
          <CheckIcon className="size-3" strokeWidth={3} />
        </span>
      ) : (
        <span className="flex size-5 items-center justify-center rounded-full bg-muted text-[11px] font-semibold text-muted-foreground" aria-hidden="true">
          {n}
        </span>
      )}
      {title}
    </h2>
  )
}

function KeyPicker({ r, children }: { r: Recover; children: (open: () => void) => ReactNode }) {
  const input = useRef<HTMLInputElement>(null)
  const [over, setOver] = useState(false)
  const drop = (e: DragEvent) => {
    e.preventDefault()
    setOver(false)
    const f = e.dataTransfer.files[0]
    if (f) void r.chooseFile(f)
  }
  return (
    <div
      onDragOver={(e) => {
        e.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={drop}
      className={cn('rounded-2xl transition-shadow duration-(--motion-fast) ease-standard', over && 'ring-2 ring-primary/50')}
    >
      <input
        ref={input}
        type="file"
        accept=".txt,text/plain"
        className="sr-only"
        tabIndex={-1}
        aria-hidden="true"
        onChange={(e) => {
          const f = e.target.files?.[0]
          if (f) void r.chooseFile(f)
          e.target.value = ''
        }}
      />
      {children(() => input.current?.click())}
    </div>
  )
}

function S3Fields({ r, phone }: { r: Recover; phone?: boolean }) {
  const w = r.where
  const size = phone ? 'lg' : 'default'
  const bad = (k: string) => r.problem?.field === k || undefined
  return (
    <div className="flex flex-col gap-3">
      <div className={cn('grid gap-3', phone ? 'grid-cols-1' : 'grid-cols-[1fr_minmax(0,0.45fr)]')}>
        <Field id="recover-endpoint" label={t('offsite.endpoint')}>
          <Input id="recover-endpoint" value={w.endpoint} onChange={(e) => r.set('endpoint', e.target.value)} placeholder={t('offsite.endpointPlaceholder')} inputMode="url" spellCheck={false} autoComplete="off" aria-invalid={bad('endpoint')} size={size} />
        </Field>
        <Field id="recover-bucket" label={t('offsite.bucket')}>
          <Input id="recover-bucket" value={w.bucket} onChange={(e) => r.set('bucket', e.target.value)} spellCheck={false} autoComplete="off" aria-invalid={bad('bucket')} size={size} />
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field id="recover-keyid" label={t('offsite.keyId')}>
          <Input id="recover-keyid" value={w.accessKeyId} onChange={(e) => r.set('accessKeyId', e.target.value)} spellCheck={false} autoComplete="off" aria-invalid={bad('accessKeyId')} size={size} />
        </Field>
        <Field id="recover-secret" label={t('offsite.secretKey')}>
          <Input id="recover-secret" type="password" value={w.secretKey} onChange={(e) => r.set('secretKey', e.target.value)} autoComplete="new-password" aria-invalid={bad('secretKey')} size={size} />
        </Field>
      </div>
    </div>
  )
}

function SFTPFields({ r, phone }: { r: Recover; phone?: boolean }) {
  const w = r.where
  const size = phone ? 'lg' : 'default'
  const bad = (k: string) => r.problem?.field === k || undefined
  return (
    <div className="flex flex-col gap-3">
      <div className={cn('grid gap-3', phone ? 'grid-cols-[1fr_84px]' : 'grid-cols-[1fr_72px]')}>
        <Field id="recover-host" label={t('offsite.host')}>
          <Input id="recover-host" value={w.host} onChange={(e) => r.set('host', e.target.value)} placeholder={t('offsite.hostPlaceholder')} spellCheck={false} autoComplete="off" aria-invalid={bad('host')} size={size} />
        </Field>
        <Field id="recover-port" label={t('offsite.port')}>
          <Input id="recover-port" value={w.port} onChange={(e) => r.set('port', e.target.value)} inputMode="numeric" maxLength={5} autoComplete="off" aria-invalid={bad('port')} size={size} />
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field id="recover-user" label={t('offsite.user')}>
          <Input id="recover-user" value={w.user} onChange={(e) => r.set('user', e.target.value)} spellCheck={false} autoComplete="off" aria-invalid={bad('user')} size={size} />
        </Field>
        <Field id="recover-password" label={t('offsite.password')}>
          <Input id="recover-password" type="password" value={w.password} onChange={(e) => r.set('password', e.target.value)} autoComplete="new-password" aria-invalid={bad('password')} size={size} />
        </Field>
      </div>
      <Field id="recover-folder" label={t('offsite.folder')}>
        <Input id="recover-folder" value={w.folder} onChange={(e) => r.set('folder', e.target.value)} placeholder={t('recover.folderPlaceholder')} spellCheck={false} autoComplete="off" aria-invalid={bad('folder')} size={size} />
      </Field>
      <p className="text-xs text-muted-foreground">{t('recover.sftpPassword')}</p>
    </div>
  )
}

function ConnectLine({ r, phone }: { r: Recover; phone?: boolean }) {
  const ws = useWorkspace()
  const where = r.problem && r.problem.field !== 'recoveryKey' ? r.problem : undefined
  if (r.view) {
    const found = r.view.copies.length
    return (
      <p className={cn('flex animate-fade items-center gap-2 text-[13px]', found ? 'text-foreground' : 'text-warning-foreground')}>
        <CircleCheckIcon className={cn('size-4 shrink-0', found ? 'text-success-foreground' : 'text-warning-foreground')} aria-hidden="true" />
        {found ? t('recover.connected', { count: found, server: r.view.server }) : t('recover.noCopies', { server: r.view.server })}
      </p>
    )
  }
  return (
    <div className="flex flex-col gap-3">
      {where && <ProblemLine problem={where} />}
      <Button
        variant="outline"
        size={phone ? 'touch' : 'default'}
        className={cn(!phone && 'self-start')}
        disabledReason={noAgent(ws) ? t('reason.noAgent') : !r.key ? t('recover.keyFirst') : !ready(r.where) ? t('reason.fillIn') : undefined}
        loading={r.busy}
        onClick={() => void r.connect()}
      >
        {t('recover.connect')}
      </Button>
    </div>
  )
}

function CopyPicker({ r, phone }: { r: Recover; phone?: boolean }) {
  const [all, setAll] = useState(false)
  const copies = r.view?.copies ?? []
  const shown = all ? copies : copies.slice(0, newestShown)
  if (!r.view) return <p className="text-[13px] text-muted-foreground max-sm:px-4">{t('recover.pickFirst')}</p>
  const rowClass = phone ? 'min-h-14 items-center gap-3 rounded-none border-0 border-b border-border px-4 py-2 shadow-none last:border-b-0 has-[[data-checked]]:border-border has-[[data-checked]]:bg-transparent has-[[data-checked]]:shadow-none' : 'items-start gap-3 px-3.5 py-2.5'
  return (
    <div className="flex animate-fade flex-col gap-2">
      <CardGroup value={r.picked ?? ''} onChange={r.setPicked} label={t('recover.step.pick')} className={cn('flex flex-col', phone ? 'overflow-hidden rounded-3xl border border-border bg-white' : 'gap-2')}>
        {shown.map((c) => (
          <ChoiceCard key={c.name} value={c.name} radio="start" className={rowClass}>
            <span className={cn('block font-semibold', phone ? 'text-base font-normal' : 'text-[13px]')}>{formatDay(c.createdAt)}</span>
            <span className={cn('block text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{t('recover.copySize', { size: formatBytes(c.sizeBytes) })}</span>
          </ChoiceCard>
        ))}
      </CardGroup>
      {copies.length > newestShown && (
        <button type="button" onClick={() => setAll(!all)} className="inline-flex min-h-8 items-center gap-1 self-start rounded-md text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring max-sm:px-4">
          {all ? t('world.showFewer') : t('world.showAll', { count: copies.length })}
          {all ? <ChevronUpIcon className="size-3.5" aria-hidden="true" /> : <ChevronDownIcon className="size-3.5" aria-hidden="true" />}
        </button>
      )}
    </div>
  )
}

function nextLabel(r: Recover): string {
  return r.op?.status === 'running' ? t('recover.showProgress') : t('recover.next')
}

const noAgent = (ws: Workspace) => ws.stale || !ws.machine

/** Why Next can't be pressed yet; a running restore can always be shown. */
function nextReason(r: Recover, ws: Workspace): string | undefined {
  if (r.op?.status === 'running') return undefined
  if (noAgent(ws)) return t('reason.noAgent')
  if (!r.view) return t('recover.findFirst')
  if (r.view.copies.length === 0) return t('recover.noCopies', { server: r.view.server })
  return r.copy ? undefined : t('recover.pickCopy')
}

function Summary({ r }: { r: Recover }) {
  const ws = useWorkspace()
  const none = <span className="text-muted-foreground">{t('common.notPicked')}</span>
  const rows: { label: string; value: ReactNode }[] = [
    { label: t('recover.row.key'), value: r.key ? [r.key.server, t('recover.keys', { count: r.key.keys })].filter(Boolean).join(t('common.dot')) : none },
    { label: t('recover.row.from'), value: r.view ? r.view.place : none },
    { label: t('recover.row.copy'), value: r.copy ? formatDay(r.copy.createdAt) : none },
    { label: t('recover.row.size'), value: r.copy ? formatBytes(r.copy.sizeBytes) : none },
  ]
  return (
    <Card className="p-4">
      <div className="flex items-center gap-3">
        <Pip pose="wave" size={44} />
        <div>
          <div className="text-[15px] font-semibold">{r.key?.server ? t('recover.summary', { server: r.key.server }) : t('recover.summaryAny')}</div>
          <div className="text-xs text-muted-foreground">{t('new.onMachine', { machine: ws.machineName })}</div>
        </div>
      </div>
      <dl className="mt-4 flex flex-col gap-2.5 border-t border-border pt-4 text-xs">
        {rows.map((x) => (
          <div key={x.label} className="flex items-center justify-between gap-3">
            <dt className="text-muted-foreground">{x.label}</dt>
            <dd className="min-w-0 truncate text-right font-medium">{x.value}</dd>
          </div>
        ))}
      </dl>
    </Card>
  )
}

function Dialogs({ r, phone }: { r: Recover; phone: boolean }) {
  return (
    <>
      {r.ask && <HostKeyDialog host={r.where.host.trim()} hostKey={r.ask} phone={phone} busy={r.busy} onConfirm={r.confirmHostKey} onClose={() => r.setAsk(undefined)} />}
      {r.op && <CopyJobDialog op={r.op} open={r.jobOpen} loadError={r.loadError} onHide={r.hideJob} date={r.copy?.createdAt} sizeBytes={typeof r.op.detail?.size === 'number' ? r.op.detail.size : r.copy?.sizeBytes} place={r.view?.place ?? ''} background={t('recover.keepOpen')} />}
      <RestoreDialog preview={r.preview} onClose={r.closePreview} />
    </>
  )
}

function DesktopRecover({ r }: { r: Recover }) {
  const ws = useWorkspace()
  const keyProblem = r.problem?.field === 'recoveryKey' ? r.problem : undefined
  return (
    <div className="grid gap-6 xl:grid-cols-[1fr_280px]">
      <Card className="flex min-w-0 flex-col gap-6 p-5">
        <section className="flex flex-col gap-3">
          <StepHead n={1} done={!!r.key} title={t('recover.step.key')} />
          <div className="pl-[30px]">
            <KeyPicker r={r}>
              {(open) =>
                r.key ? (
                  <div className="flex items-center gap-3 rounded-2xl border border-border px-4 py-3">
                    <KeyRoundIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[13px] font-semibold">{r.key.name}</span>
                      <span className="block text-xs text-muted-foreground">{[r.key.server, keysText(r.key)].filter(Boolean).join(t('common.dot'))}</span>
                    </span>
                    <Button variant="ghost" size="sm" onClick={open}>
                      {t('common.change')}
                    </Button>
                  </div>
                ) : (
                  <button type="button" onClick={open} className="flex w-full items-center gap-3 rounded-2xl border border-dashed border-input px-4 py-4 text-left outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring">
                    <UploadIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                    <span className="min-w-0">
                      <span className="block text-[13px] font-semibold">{t('recover.choose')}</span>
                      <span className="block text-xs text-muted-foreground">{t('recover.chooseHint')}</span>
                    </span>
                  </button>
                )
              }
            </KeyPicker>
            {keyProblem && (
              <div className="mt-3">
                <ProblemLine problem={keyProblem} />
              </div>
            )}
          </div>
        </section>
        <section className="flex flex-col gap-3">
          <StepHead n={2} done={!!r.view} title={t('recover.step.where')} />
          <div className="flex flex-col gap-4 pl-[30px]">
            <CardGroup value={r.where.type} onChange={(x) => r.set('type', x)} label={t('recover.step.where')} className="grid grid-cols-2 gap-2">
              {(['s3', 'sftp'] as const).map((x) => (
                <ChoiceCard key={x} value={x} radio="start" className="items-center gap-3 px-3.5 py-2.5">
                  <span className="block text-[13px] font-semibold">{t(x === 's3' ? 'offsite.s3' : 'offsite.sftp')}</span>
                </ChoiceCard>
              ))}
            </CardGroup>
            {r.where.type === 's3' ? <S3Fields r={r} /> : <SFTPFields r={r} />}
            <ConnectLine r={r} />
          </div>
        </section>
        <section className="flex flex-col gap-3">
          <StepHead n={3} done={false} title={t('recover.step.pick')} />
          <div className="pl-[30px]">
            <CopyPicker r={r} />
          </div>
        </section>
        <div className="flex items-center gap-3 border-t border-border pt-4">
          <span className="text-xs text-muted-foreground">{t('recover.note')}</span>
          <Button className="ml-auto" disabledReason={nextReason(r, ws)} loading={r.busy && !!r.view} onClick={() => void r.start()}>
            {nextLabel(r)}
            <ArrowRightIcon />
          </Button>
        </div>
      </Card>
      <aside className="self-start">
        <Summary r={r} />
      </aside>
    </div>
  )
}

function PhoneRow({ icon, title, hint, onClick }: { icon: ReactNode; title: string; hint: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="flex min-h-16 w-full items-center gap-3 rounded-3xl border border-border bg-white px-4 py-2 text-left">
      <span className="text-muted-foreground [&_svg]:size-5" aria-hidden="true">
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-base">{title}</span>
        <span className="block truncate text-[13px] text-muted-foreground">{hint}</span>
      </span>
      <CircleCheckIcon className="size-5 shrink-0 text-success-foreground" aria-hidden="true" />
    </button>
  )
}

function PhoneRecover({ r }: { r: Recover }) {
  const ws = useWorkspace()
  const [editing, setEditing] = useState(false)
  const dot = t('common.dot')
  const keyProblem = r.problem?.field === 'recoveryKey' ? r.problem : undefined
  const found = r.view && !editing
  return (
    <div className="flex flex-col gap-4 pb-28">
      <PhoneBackHeader to={{ name: 'home' }} label={t('nav.home')} title={t('recover.phoneTitle')} />
      <section className="flex flex-col gap-2">
        <SectionLabel className="px-4">{`1${dot}${t('recover.step.key')}`}</SectionLabel>
        <KeyPicker r={r}>
          {(open) =>
            r.key ? (
              <PhoneRow icon={<KeyRoundIcon />} title={r.key.server ? t('recover.phoneKey', { server: r.key.server }) : r.key.name} hint={keysText(r.key)} onClick={open} />
            ) : (
              <Button size="touch" variant="outline" className="w-full" onClick={open}>
                <UploadIcon />
                {t('recover.choose')}
              </Button>
            )
          }
        </KeyPicker>
        {keyProblem && (
          <div className="px-1">
            <ProblemLine problem={keyProblem} />
          </div>
        )}
      </section>
      <section className="flex flex-col gap-2">
        <SectionLabel className="px-4">{`2${dot}${t('recover.step.where')}`}</SectionLabel>
        {found && r.view ? (
          <PhoneRow
            icon={<HardDriveIcon />}
            title={r.view.place}
            hint={t('recover.phoneFound', { count: r.view.copies.length, where: r.where.type === 's3' ? r.where.bucket.trim() : r.where.host.trim() })}
            onClick={() => setEditing(true)}
          />
        ) : (
          <div className="flex flex-col gap-4 rounded-3xl border border-border bg-white p-4">
            <ChoiceSelect
              className="w-full"
              label={t('recover.step.where')}
              value={r.where.type}
              onChange={(x) => r.set('type', x)}
              options={[
                { value: 's3', label: t('offsite.s3') },
                { value: 'sftp', label: t('offsite.sftp') },
              ]}
            />
            {r.where.type === 's3' ? <S3Fields r={r} phone /> : <SFTPFields r={r} phone />}
            <ConnectLine r={r} phone />
            {r.view && (
              <Button variant="ghost" size="touch" onClick={() => setEditing(false)}>
                {t('common.done')}
              </Button>
            )}
          </div>
        )}
      </section>
      <section className="flex flex-col gap-2">
        <SectionLabel className="px-4">{`3${dot}${t('recover.step.pick')}`}</SectionLabel>
        <CopyPicker r={r} phone />
      </section>
      <p className="px-1 text-[13px] text-muted-foreground">{t('recover.note')}</p>
      <PhoneActions>
        <Button size="touch" disabledReason={nextReason(r, ws)} loading={r.busy && !!r.view} onClick={() => void r.start()}>
          {nextLabel(r)}
          <ArrowRightIcon />
        </Button>
      </PhoneActions>
    </div>
  )
}

/** A new machine brings a server back from its copies with the recovery key file. */
export function RecoverPage() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const r = useRecover()
  if (!can(ws.me, 'backups.recover')) {
    return (
      <>
        {phone ? <PhoneBackHeader to={{ name: 'home' }} label={t('nav.home')} title={t('recover.phoneTitle')} /> : <PageHeader title={t('recover.title')} subtitle={t('recover.on', { machine: ws.machineName })} />}
        <PageBody>
          <Notice title={t('recover.holdersOnly')}>{t('recover.holdersOnlyHint')}</Notice>
        </PageBody>
      </>
    )
  }
  return (
    <>
      {phone ? (
        <PhoneRecover r={r} />
      ) : (
        <>
          <PageHeader
            title={t('recover.title')}
            subtitle={t('recover.on', { machine: ws.machineName })}
            actions={
              <Button variant="ghost" render={<a {...linkProps({ name: 'home' })} />}>
                {t('common.cancel')}
                <XIcon />
              </Button>
            }
          />
          <PageBody>
            <DesktopRecover r={r} />
          </PageBody>
        </>
      )}
      <Dialogs r={r} phone={phone} />
    </>
  )
}

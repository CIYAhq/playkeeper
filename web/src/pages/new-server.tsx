import { useEffect, useState, type ReactNode } from 'react'
import { ArrowLeftIcon, ArrowRightIcon, CheckIcon, ChevronLeftIcon, ExternalLinkIcon, Gamepad2Icon, RefreshCwIcon, XIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { get, post } from '@/api/client'
import type { Operation, RestorePreview, ServerStatus } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { GameIcon, Pip, TypeLogo } from '@/components/app/art'
import { Card, Notice } from '@/components/app/bits'
import { CardGroup, ChoiceCard, Stepper, useIsPhone } from '@/components/app/controls'
import { createBlocked, createRequest, EulaCheck, freeName, MemoryBar, MemoryReadout, MemorySlider, memoryOptions, MoreOptions, recommendedVersion, StyleCards, styleMemory, TypeCards, VersionPicker, versionBlocked, type CreateChoices } from '@/components/app/create'
import { PhoneActions } from '@/components/app/frame'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { PageBody, PageHeader } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatMB, relativeTime } from '@/lib/format'
import { linkProps, navigate } from '@/lib/router'
import { typeName } from '@/lib/servers'
import { preset } from '@/lib/styles'
import { cn } from '@/lib/utils'

const stepKeys: MessageKey[] = ['new.step.type', 'new.step.version', 'new.step.style', 'new.step.memory', 'new.step.name']
const nextKeys: MessageKey[] = ['new.nextVersion', 'new.nextStyle', 'new.nextMemory', 'new.nextName']
const continueKeys: MessageKey[] = ['new.continueVersion', 'new.continueStyle', 'new.continueMemory', 'new.continueName']
const noteKeys: MessageKey[] = ['new.note.type', 'new.note.version', 'new.note.version', 'new.note.memory', 'new.note.name']

/** Waits for a new server to show up in the list, then opens it. */
async function openCreated(op: Operation, refresh: () => Promise<void>) {
  await refresh()
  for (let i = 0; i < 10; i++) {
    const list = await get<ServerStatus[]>('/api/servers').catch(() => [])
    const s = list.find((x) => x.id === op.serverId)
    if (s) {
      navigate({ name: 'server', slug: s.slug, tab: 'overview' })
      return
    }
    await new Promise((r) => window.setTimeout(r, 500))
  }
  navigate({ name: 'home' })
}

export function NewServerPage() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const { catalog, error, reload } = useCatalog(ws.machine?.id, { fresh: true })
  const [step, setStep] = useState(0)
  const [c, setC] = useState<CreateChoices>()
  const [nameEdited, setNameEdited] = useState(false)
  const [busy, setBusy] = useState(false)
  const [createError, setCreateError] = useState<string>()
  const [restoreOpen, setRestoreOpen] = useState(false)
  const [preview, setPreview] = useState<RestorePreview>()

  useEffect(() => {
    if (!catalog || c) return
    const style = 'friends'
    const p = preset(style)
    const name = freeName(p ? t(p.name) : t('nav.newServer'), ws.servers)
    setC({ type: 'paper', versionId: recommendedVersion(catalog)?.id ?? '', acceptExperimental: false, style, hardcore: false, levelType: 'normal', memoryMB: styleMemory(catalog, style), name, motd: '', eula: false })
  }, [catalog, c, ws.servers])

  const update = (patch: Partial<CreateChoices>) => setC((prev) => (prev ? { ...prev, ...patch } : prev))
  const options = memoryOptions(catalog)
  const version = catalog?.versions.find((v) => v.id === c?.versionId)
  const noMemory = !!catalog && options.length === 0

  function blocked(): string | undefined {
    if (!c) return t('common.loading')
    switch (step) {
      case 0:
        return c.type === 'paper' ? undefined : t('common.comingSoon')
      case 1:
        return versionBlocked(c, version)
      case 2:
        return undefined
      case 3:
        return noMemory || c.memoryMB <= 0 ? t('home.newServerFull', { machine: ws.machineName }) : undefined
      default:
        return createBlocked(c, version)
    }
  }

  async function create() {
    if (!c || !ws.machine) return
    setBusy(true)
    setCreateError(undefined)
    try {
      const op = await post<Operation>(machineApi(ws.machine.id, '/servers'), createRequest(c))
      await openCreated(op, ws.refresh)
    } catch (e) {
      setCreateError(errorText(e))
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const next = () => (step === 4 ? void create() : setStep((s) => s + 1))
  const back = () => setStep((s) => Math.max(0, s - 1))
  const stepTitles = stepKeys.map((k) => t(k))

  let body: ReactNode
  if (error) {
    body = (
      <Notice
        tone="error"
        title={t('new.versionsError')}
        action={
          <Button variant="outline" size="sm" onClick={reload}>
            <RefreshCwIcon />
            {t('common.tryAgain')}
          </Button>
        }
      >
        {error}
      </Notice>
    )
  } else if (!c || !catalog) {
    body = <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
  } else {
    switch (step) {
      case 0:
        body = (
          <div className="flex flex-col gap-6">
            {phone ? (
              <>
                <div>
                  <h1 className="text-[26px] leading-8 font-extrabold tracking-[-0.02em]">{t('new.typeQuestion')}</h1>
                  <p className="mt-1 text-[15px] text-muted-foreground">{t('new.typeLeadPhone')}</p>
                </div>
                <GameCard phone />
              </>
            ) : (
              <section>
                <h2 className="text-[15px] font-semibold">
                  {t('new.game')} <span className="ml-1 text-[13px] font-normal text-muted-foreground">{t('new.gameHint')}</span>
                </h2>
                <div className="mt-2.5 grid grid-cols-2 gap-2.5">
                  <GameCard />
                  <div className="flex items-center gap-3 rounded-2xl border border-dashed border-input bg-warm px-4 py-3 text-muted-foreground">
                    <Gamepad2Icon className="size-5" aria-hidden="true" />
                    <span>
                      <span className="block text-sm font-semibold text-foreground/80">{t('new.moreGames')}</span>
                      <span className="block text-xs">{t('new.moreGamesHint')}</span>
                    </span>
                  </div>
                </div>
              </section>
            )}
            <section>
              {phone ? (
                <div className="section-label mb-2">{t('new.typeTitle')}</div>
              ) : (
                <div className="flex items-end justify-between gap-3">
                  <div>
                    <h2 className="text-[15px] font-semibold">{t('new.typeTitle')}</h2>
                    <p className="mt-0.5 text-[13px] text-muted-foreground">{t('new.typeHint')}</p>
                  </div>
                  <a href={t('new.differenceUrl')} target="_blank" rel="noreferrer" className="inline-flex shrink-0 items-center gap-1 text-xs font-medium text-primary hover:underline">
                    {t('new.difference')}
                    <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
                  </a>
                </div>
              )}
              <div className={phone ? '' : 'mt-3'}>
                <TypeCards catalog={catalog} value={c.type} onChange={(type) => update({ type })} phone={phone} />
              </div>
            </section>
          </div>
        )
        break
      case 1:
        body = (
          <div className="flex flex-col gap-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('new.versionTitle')}</h2>
                <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{phone ? t('new.versionLeadPhone') : t('new.versionLead')}</p>
              </div>
              <div className={cn('flex items-center gap-2 text-[13px]', phone ? 'w-full justify-between' : 'rounded-full border border-border bg-muted py-1 pr-3 pl-1.5')}>
                <span className="flex items-center gap-2 font-medium">
                  <TypeLogo type={c.type} size={phone ? 28 : 22} />
                  {t('new.versions', { type: typeName(c.type) })}
                </span>
                <button type="button" onClick={() => setStep(0)} className="font-semibold text-primary hover:underline max-sm:text-[15px] max-sm:text-success-strong">
                  {t('new.changeType')}
                </button>
              </div>
            </div>
            <VersionPicker catalog={catalog} servers={ws.servers} value={c.versionId} onChange={(versionId) => update({ versionId, acceptExperimental: false })} acceptExperimental={c.acceptExperimental} onAcceptExperimental={(acceptExperimental) => update({ acceptExperimental })} phone={phone} />
            {phone ? (
              <p className="mt-2 text-[13px] text-muted-foreground">{t('new.forwardPhone')}</p>
            ) : (
              <div className="grid gap-4 sm:grid-cols-[1fr_1fr]">
                <div />
                <div>
                  <h3 className="text-[13px] font-semibold">{t('new.forward')}</h3>
                  <p className="mt-1 text-xs text-muted-foreground">{t('new.forwardBody')}</p>
                  {catalog.versionsCheckedAt && <p className="mt-2 text-xs text-muted-foreground">{t('new.listFrom', { time: relativeTime(catalog.versionsCheckedAt) })}</p>}
                </div>
              </div>
            )}
          </div>
        )
        break
      case 2:
        body = (
          <div className="flex flex-col gap-4">
            <div>
              <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('style.question')}</h2>
              <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{phone ? t('style.leadPhone') : t('style.lead')}</p>
            </div>
            <StyleCards
              catalog={catalog}
              value={c.style}
              onChange={(style) => {
                const p = preset(style)
                update({ style, memoryMB: styleMemory(catalog, style), ...(nameEdited || !p ? {} : { name: freeName(t(p.name), ws.servers) }) })
              }}
              phone={phone}
            />
            <MoreOptions hardcore={c.hardcore} onHardcore={(hardcore) => update({ hardcore })} level={c.levelType} onLevel={(levelType) => update({ levelType })} phone={phone} />
          </div>
        )
        break
      case 3: {
        const suggested = styleMemory(catalog, c.style)
        const others = catalog.servers.filter((x) => !x.running)
        body = (
          <div className="flex flex-col gap-4">
            <div>
              <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('new.memoryTitle')}</h2>
              <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('new.memoryLead', { style: t(preset(c.style)?.title ?? 'style.friends.title').toLowerCase(), memory: formatMB(suggested) })}</p>
            </div>
            {noMemory ? (
              <Notice tone="warning" title={t('new.noMemoryTitle')}>
                {t('new.noMemory', { machine: ws.machineName })}
              </Notice>
            ) : (
              <>
                <Card className="p-4">
                  <h3 className="text-sm font-semibold">{t('new.machineHas', { machine: ws.machineName, total: formatMB(catalog.hostMemoryMB) })}</h3>
                  <p className="mt-0.5 mb-3 text-xs text-muted-foreground">{others[0] ? t('new.shareHintStopped', { server: others[0].name }) : t('new.shareHint')}</p>
                  <MemoryBar catalog={catalog} memoryMB={c.memoryMB} />
                </Card>
                <Card className="p-4">
                  <h3 className="text-sm font-semibold">{t('new.memoryFor')}</h3>
                  <p className="mt-0.5 text-xs text-muted-foreground">{t('new.memoryForHint')}</p>
                  <div className="mt-5 grid items-center gap-6 md:grid-cols-[1fr_200px]">
                    <MemorySlider options={options} value={c.memoryMB} onChange={(memoryMB) => update({ memoryMB })} />
                    <div className="md:border-l md:border-border md:pl-5">
                      <MemoryReadout memoryMB={c.memoryMB} recommended={c.memoryMB === suggested} style={c.style} />
                    </div>
                  </div>
                  {c.memoryMB === options[options.length - 1] && <p className="mt-4 text-xs text-muted-foreground">{t('new.maxNote')}</p>}
                </Card>
              </>
            )}
          </div>
        )
        break
      }
      default:
        body = (
          <div className="flex flex-col gap-4">
            <div>
              <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('new.nameTitle')}</h2>
              <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('new.nameLead')}</p>
            </div>
            <label className="flex flex-col gap-1.5 text-[13px] font-medium">
              {t('new.nameLabel')}
              <Input
                value={c.name}
                onChange={(e) => {
                  setNameEdited(true)
                  update({ name: e.target.value })
                }}
                maxLength={32}
                autoComplete="off"
                className="max-w-[360px] max-sm:max-w-none"
              />
            </label>
            <label className="flex flex-col gap-1.5 text-[13px] font-medium">
              {t('new.motdLabel')}
              <Input value={c.motd} onChange={(e) => update({ motd: e.target.value })} maxLength={59} placeholder={c.name || t('new.motdPlaceholder')} autoComplete="off" className="max-w-[360px] max-sm:max-w-none" />
            </label>
            <EulaCheck checked={c.eula} onChange={(eula) => update({ eula })} className="mt-2 rounded-2xl border border-border p-3.5 max-sm:bg-white" />
            {createError && (
              <p className="text-[13px] text-destructive-foreground" role="alert">
                {createError}
              </p>
            )}
          </div>
        )
    }
  }

  const summary = c && catalog && <Summary choices={c} step={step} port={catalog.suggestedPort} version={version?.minecraftVersion ?? ''} />
  const restoreLink = (
    <p className="text-xs text-muted-foreground">
      {rich('restore.newLink', {
        restore: (chunk) => (
          <button type="button" className="font-medium text-success-strong hover:underline" onClick={() => setRestoreOpen(true)}>
            {chunk}
          </button>
        ),
      })}
    </p>
  )
  const restore = (
    <>
      <Dialog open={restoreOpen} onOpenChange={setRestoreOpen}>
        <DialogPopup className="sm:max-w-[520px]">
          <div className="flex items-start gap-4 px-6 pt-6 pb-2">
            <Pip pose="box" size={52} />
            <div className="min-w-0 pt-1">
              <DialogTitle className="text-lg font-bold">{t('new.fromBackup')}</DialogTitle>
              <DialogDescription className="mt-0.5 text-[13px]">{t('restore.newLead')}</DialogDescription>
            </div>
          </div>
          <DialogPanel className="pt-3">
            <RestoreDropZone
              onPreview={(p) => {
                setRestoreOpen(false)
                setPreview(p)
              }}
            />
          </DialogPanel>
        </DialogPopup>
      </Dialog>
      <RestoreDialog preview={preview} onClose={() => setPreview(undefined)} />
    </>
  )

  if (phone) {
    return (
      <div className="flex flex-col pb-28">
        <header className="flex items-center gap-2 pt-3 pb-2">
          {step === 0 ? (
            <Button variant="ghost" size="icon-lg" aria-label={t('new.cancel')} render={<a {...linkProps({ name: 'home' })} />}>
              <XIcon className="size-5" />
            </Button>
          ) : (
            <Button variant="ghost" size="icon-lg" aria-label={t('common.back')} onClick={back}>
              <ChevronLeftIcon className="size-5" />
            </Button>
          )}
          <div className="min-w-0">
            <h1 className="text-[17px] leading-5 font-bold">{t('new.title')}</h1>
            <p className="text-[13px] text-muted-foreground">{t('new.stepOf', { n: step + 1, total: 5, step: stepTitles[step] ?? '' })}</p>
          </div>
        </header>
        <div className="mb-5 flex gap-1.5" aria-hidden="true">
          {stepTitles.map((s, i) => (
            <span key={s} className={cn('h-1 flex-1 rounded-full', i <= step ? 'bg-primary' : 'bg-foreground/10')} />
          ))}
        </div>
        {body}
        <div className="mt-6">{restoreLink}</div>
        <PhoneActions>
          <Button size="touch" onClick={next} disabledReason={blocked()} loading={busy}>
            {step === 4 ? t('new.create', { name: c?.name.trim() || t('nav.newServer') }) : step === 0 ? t('new.continueVersion') : t('common.continue')}
            <ArrowRightIcon />
          </Button>
        </PhoneActions>
        {restore}
      </div>
    )
  }

  return (
    <>
      <PageHeader
        breadcrumb={
          <span className="flex items-center gap-1.5">
            {ws.machineName}
            <span className="text-muted-foreground/60" aria-hidden="true">
              /
            </span>
            <span className="font-semibold text-foreground">{t('new.title')}</span>
          </span>
        }
        title={t('new.title')}
        subtitle={t('new.lead', { machine: ws.machineName })}
        actions={
          <Button variant="ghost" render={<a {...linkProps({ name: 'home' })} />}>
            {t('new.cancel')}
            <XIcon />
          </Button>
        }
      />
      <PageBody className="flex flex-col gap-5">
        <Stepper steps={stepTitles} current={step} label={t('new.steps')} />
        <div className="grid gap-6 xl:grid-cols-[1fr_280px]">
          <div className="flex min-w-0 flex-col">
            {body}
            <div className="mt-6 flex items-center gap-3 border-t border-border pt-4">
              {step > 0 && (
                <Button variant="ghost" onClick={back}>
                  <ArrowLeftIcon />
                  {t('common.back')}
                </Button>
              )}
              <span className="ml-auto text-xs text-muted-foreground">{step < 4 ? t(nextKeys[step] ?? 'new.nextName', { type: typeName(c?.type) }) : ''}</span>
              <Button onClick={next} disabledReason={blocked()} loading={busy}>
                {step < 4 ? t(continueKeys[step] ?? 'new.continueName') : t('new.create', { name: c?.name.trim() || t('nav.newServer') })}
                <ArrowRightIcon />
              </Button>
            </div>
            <div className="mt-4">{restoreLink}</div>
          </div>
          <aside className="self-start">
            {summary}
            <p className="mt-3 px-1 text-xs text-muted-foreground">{t(noteKeys[step] ?? 'new.note.name')}</p>
          </aside>
        </div>
      </PageBody>
      {restore}
    </>
  )
}

function GameCard({ phone }: { phone?: boolean }) {
  return (
    <CardGroup value="java" onChange={() => undefined} label={t('new.game')}>
      <ChoiceCard value="java" className="items-center gap-3 p-3">
        <span className="flex items-center gap-3">
          <GameIcon size={phone ? 44 : 40} />
          <span>
            <span className="block text-sm font-semibold max-sm:text-base">{t('new.java')}</span>
            <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{phone ? t('new.javaHintPhone') : t('new.javaHint')}</span>
          </span>
        </span>
      </ChoiceCard>
    </CardGroup>
  )
}

function Summary({ choices: c, step, port, version }: { choices: CreateChoices; step: number; port?: number; version: string }) {
  const ws = useWorkspace()
  const p = preset(c.style)
  const v = (done: boolean, value: string) =>
    done ? (
      <span className="flex items-center gap-1 font-medium">
        {value}
        <CheckIcon className="size-3.5 text-primary" aria-hidden="true" />
      </span>
    ) : step >= 0 && value ? (
      <span className="font-semibold text-success-foreground">{value}</span>
    ) : (
      <span className="text-muted-foreground">{t('common.notPicked')}</span>
    )
  const rows: { label: string; value: ReactNode }[] = [
    { label: t('new.row.game'), value: v(true, t('new.gameValue')) },
    { label: t('new.row.type'), value: v(step > 0, typeName(c.type)) },
    { label: t('new.row.version'), value: step >= 1 ? v(step > 1, version) : v(false, '') },
    { label: t('new.row.style'), value: step >= 2 ? v(step > 2, p ? t(p.title) : '') : v(false, '') },
    { label: t('new.row.memory'), value: step >= 3 ? v(step > 3, formatMB(c.memoryMB)) : v(false, '') },
    { label: t('new.row.name'), value: step >= 4 ? v(false, c.name) : v(false, '') },
  ]
  if (step >= 3 && port) rows.push({ label: t('new.row.port'), value: <span className="text-muted-foreground">{t('new.portPicked', { port })}</span> })
  return (
    <Card className="p-4">
      <div className="flex items-center gap-3">
        <Pip pose="wave" size={44} />
        <div>
          <div className="text-[15px] font-semibold">{t('new.summary')}</div>
          <div className="text-xs text-muted-foreground">{t('new.onMachine', { machine: ws.machineName })}</div>
        </div>
      </div>
      <dl className="mt-4 flex flex-col gap-2.5 border-t border-border pt-4 text-xs">
        {rows.map((r) => (
          <div key={r.label} className="flex items-center justify-between gap-3">
            <dt className="text-muted-foreground">{r.label}</dt>
            <dd>{r.value}</dd>
          </div>
        ))}
      </dl>
    </Card>
  )
}

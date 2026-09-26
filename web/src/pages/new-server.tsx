import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { ArrowLeftIcon, ArrowRightIcon, CheckIcon, ChevronLeftIcon, ChevronRightIcon, Gamepad2Icon, RefreshCwIcon, XIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { ApiError, get, post } from '@/api/client'
import { useBuilds } from '@/api/software'
import { planTemplate } from '@/api/templates'
import type { Operation, RestorePreview, ServerStatus, TemplatePlan } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { GameIcon, Pip, TypeLogo } from '@/components/app/art'
import { Card, Notice } from '@/components/app/bits'
import { CardGroup, ChoiceCard, ChoiceSelect, Segmented, Stepper, useIsPhone } from '@/components/app/controls'
import { budgetAdvice, createBlocked, createRequest, EulaCheck, freeName, MemoryBar, MemoryReadout, MemorySlider, memoryOptions, MoreOptions, nameBlocked, recommendedVersion, StyleCards, styleMemory, TypeCards, VersionPicker, versionBlocked, type CreateChoices } from '@/components/app/create'
import { PhoneActions } from '@/components/app/frame'
import { ModpackPicker, type ModpackChoice } from '@/components/app/modpacks'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { PageBody, PageHeader } from '@/components/app/shell'
import { CardsSkeleton } from '@/components/app/skeletons'
import { BuildSelect, TypeCompare } from '@/components/app/software'
import { TemplatePicker, type TemplateChoice } from '@/components/app/templates'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { demo } from '@/lib/demo'
import { formatList, formatMB } from '@/lib/format'
import { isAway, machineLabel } from '@/lib/machines'
import { linkProps, navigate } from '@/lib/router'
import { typeName } from '@/lib/servers'
import { addonKind, hasBuilds, typeTexts } from '@/lib/software'
import { preset } from '@/lib/styles'
import { templateFromHash } from '@/lib/templates'
import { cn } from '@/lib/utils'

const stepKeys: MessageKey[] = ['new.step.type', 'new.step.version', 'new.step.style', 'new.step.memory', 'new.step.name']
const nextKeys: MessageKey[] = ['new.nextVersion', 'new.nextStyle', 'new.nextMemory', 'new.nextName']
const continueKeys: MessageKey[] = ['new.continueVersion', 'new.continueStyle', 'new.continueMemory', 'new.continueName']
const noteKeys: MessageKey[] = ['new.note.type', 'new.note.version', 'new.note.version', 'new.note.memory', 'new.note.name']

export type StartFrom = 'type' | 'modpack' | 'template'

const allStartFroms: { value: StartFrom; long: MessageKey; short: MessageKey }[] = [
  { value: 'type', long: 'new.from.type', short: 'new.from.typeShort' },
  { value: 'modpack', long: 'new.from.modpack', short: 'new.from.modpackShort' },
  { value: 'template', long: 'new.from.template', short: 'new.from.templateShort' },
]
const startFroms = allStartFroms.filter((f) => f.value === 'type' || demo?.templates !== false)

/** A server made from a pack runs the type, version and game settings the pack names; the play style step is skipped. */
export function packRequest(c: CreateChoices, pack: ModpackChoice) {
  const { name, acceptEula, memoryMB, motd, maxPlayers } = createRequest(c)
  return { name, acceptEula, memoryMB, motd, maxPlayers, acceptExperimental: false, modpack: { source: pack.source, projectId: pack.projectId, versionId: pack.versionId } }
}

/** The template decides the type, version and settings: the request names only the plan the user saw. */
function templateRequest(c: CreateChoices, choice: TemplateChoice) {
  return { name: c.name.trim(), acceptEula: c.eula, memoryMB: c.memoryMB, acceptExperimental: !!choice.plan.experimental && c.acceptExperimental, template: { fingerprint: choice.plan.fingerprint } }
}

/** The line under the summary. A modpack or template decides the type, so the name step names that type, or none when it isn't known yet. */
export function createNote(step: number, from: StartFrom, type: string | undefined): string {
  if (step === 0) return from === 'template' ? '' : from === 'modpack' ? t('new.note.modpack') : t('new.note.type')
  if (step === 4 && from !== 'type') {
    if (!type) return t('new.note.nameAny')
    return t(from === 'modpack' ? 'new.note.namePack' : 'new.note.nameTemplate', { type: typeName(type) })
  }
  if (step === 1 && addonKind(type) === 'mods') return t('new.note.versionMods')
  return t(noteKeys[step] ?? 'new.note.name', { type: typeName(type) })
}

/** "Cobblemon Modpack" names its server "Cobblemon". */
function packServerName(name: string): string {
  const short = name.replace(/\s+(mod)?pack$/i, '').trim().slice(0, 32).trim()
  return short || name.slice(0, 32).trim()
}

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

/** Makes a server on the dashboard's machine, or on the joined machine given while it's connected. */
export function NewServerPage({ machine }: { machine?: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const targets = ws.machines.filter((m) => !isAway(m))
  const target = targets.find((m) => m.id === machine) ?? ws.machine
  const machineName = target?.kind === 'remote' ? machineLabel(target) : ws.machineName
  const [c, setC] = useState<CreateChoices>()
  const { catalog, error, reload } = useCatalog(target?.id, { type: c?.type ?? 'paper', fresh: true })
  const [step, setStep] = useState(0)
  // The first step comes in with the page; later ones animate in themselves.
  const [stepped, setStepped] = useState(false)
  const [nameEdited, setNameEdited] = useState(false)
  const [busy, setBusy] = useState(false)
  const [createError, setCreateError] = useState<string>()
  const [restoreOpen, setRestoreOpen] = useState(false)
  const [preview, setPreview] = useState<RestorePreview>()
  const [compareOpen, setCompareOpen] = useState(false)
  const [handoff, setHandoff] = useState(() => templateFromHash(window.location.hash))
  useEffect(() => {
    // Once read, a shared template's data stays out of the address bar and history.
    if (templateFromHash(window.location.hash)) window.history.replaceState(null, '', window.location.pathname)
  }, [])
  const [from, setFrom] = useState<StartFrom>(handoff ? 'template' : 'type')
  const [pack, setPack] = useState<ModpackChoice>()
  const packed = from === 'modpack' && !!pack
  const [tpl, setTpl] = useState<TemplateChoice>()
  const [tplProblem, setTplProblem] = useState<string>()
  const templated = from === 'template' && !!tpl
  const chooseTemplate = useCallback((choice: TemplateChoice | undefined) => {
    setTpl(choice)
    setTplProblem(undefined)
    setHandoff(undefined)
    setC((prev) => (prev ? { ...prev, acceptExperimental: false } : prev))
  }, [])
  // The catalog keeps showing the last type's versions while the next type's load.
  const typeCatalog = catalog && c && catalog.type === c.type ? catalog : undefined
  const version = typeCatalog?.versions.find((v) => v.id === c?.versionId)
  const builds = useBuilds(target?.id, c?.type ?? 'paper', c && hasBuilds(c.type) ? version?.minecraftVersion : undefined)

  useEffect(() => {
    if (!catalog || c) return
    const style = 'friends'
    const p = preset(style)
    const name = freeName(p ? t(p.name) : t('nav.newServer'), ws.servers)
    setC({ type: 'paper', versionId: recommendedVersion(catalog)?.id ?? '', acceptExperimental: false, style, hardcore: false, levelType: 'normal', memoryMB: styleMemory(catalog, style), name, motd: '', eula: false, build: '' })
  }, [catalog, c, ws.servers])

  useEffect(() => {
    if (!typeCatalog || !c || c.versionId) return
    const rec = recommendedVersion(typeCatalog)
    if (rec) setC((prev) => (prev && !prev.versionId ? { ...prev, versionId: rec.id } : prev))
  }, [typeCatalog, c])

  const update = (patch: Partial<CreateChoices>) => setC((prev) => (prev ? { ...prev, ...patch } : prev))
  const options = memoryOptions(catalog)
  const noMemory = !!catalog && options.length === 0

  function blocked(): string | undefined {
    if (!c) return t('common.loading')
    switch (step) {
      case 0:
        if (from === 'template') {
          if (!tpl) return t('reason.templateFirst')
          if (!tpl.plan.ready) return tpl.plan.blockers[0]?.message ?? t('reason.templateBlocked')
          return tpl.plan.experimental && !c.acceptExperimental ? t('reason.experimental', { version: tpl.plan.minecraftVersion ?? '' }) : undefined
        }
        if (from === 'modpack') return pack ? undefined : t('reason.modpackFirst')
        return catalog?.types.some((ty) => ty.id === c.type && ty.available) ? undefined : t('common.comingSoon')
      case 1:
        return versionBlocked(c, version)
      case 2:
        return undefined
      case 3:
        return noMemory || c.memoryMB <= 0 ? t('home.newServerFull', { machine: machineName }) : undefined
      default:
        return packed || templated ? nameBlocked(c) : createBlocked(c, version)
    }
  }

  async function create() {
    if (!c || !target) return
    setBusy(true)
    setCreateError(undefined)
    try {
      const body = templated && tpl ? templateRequest(c, tpl) : packed && pack ? packRequest(c, pack) : createRequest(c)
      const op = await post<Operation>(machineApi(target.id, '/servers'), body)
      await openCreated(op, ws.refresh)
    } catch (e) {
      if (templated && tpl && e instanceof ApiError && e.code === 'plan_changed') {
        // The machine's versions moved on, or it forgot the plan: show the new plan before creating.
        const problem = errorText(e)
        const plan = await planTemplate(target.id, tpl.text).catch(() => undefined)
        if (plan) {
          setTpl({ ...tpl, plan })
          setTplProblem(problem)
          setStep(0)
          return
        }
      }
      setCreateError(errorText(e))
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const go = (to: number) => {
    setStepped(true)
    setStep(to)
  }
  // A pack decides the version and comes with its own mods, so it skips to memory.
  function startWithPack(p: ModpackChoice) {
    const opts = memoryOptions(catalog)
    const memoryMB = p.memoryMB ? (opts.find((mb) => mb >= p.memoryMB) ?? opts[opts.length - 1]) : undefined
    update({ ...(memoryMB ? { memoryMB } : {}), ...(nameEdited ? {} : { name: freeName(packServerName(p.name), ws.servers) }) })
    go(3)
  }
  // A template decides the type, version and settings, so it skips to memory too.
  function startWithTemplate(choice: TemplateChoice) {
    const opts = memoryOptions(catalog)
    const memoryMB = choice.plan.memoryMB ? (opts.find((mb) => mb >= choice.plan.memoryMB) ?? opts[opts.length - 1]) : undefined
    update({ ...(memoryMB ? { memoryMB } : {}), ...(nameEdited ? {} : { name: freeName(choice.plan.contents.name.slice(0, 32).trim(), ws.servers) }) })
    setTplProblem(undefined)
    go(3)
  }
  const next = () => (step === 4 ? void create() : step === 0 && templated && tpl ? startWithTemplate(tpl) : step === 0 && packed && pack ? startWithPack(pack) : go(step + 1))
  const back = () => go(step === 3 && (packed || templated) ? 0 : Math.max(0, step - 1))
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
    body = <CardsSkeleton count={6} className={cn('grid gap-2.5', phone ? 'grid-cols-1' : 'grid-cols-2 xl:grid-cols-3')} card={phone ? 'h-16' : 'h-32'} />
  } else {
    switch (step) {
      case 0:
        body = (
          <div className={cn('flex flex-col', phone && from !== 'type' ? 'gap-4' : 'gap-6')}>
            {phone && from === 'modpack' ? (
              <div>
                <h1 className="text-[26px] leading-8 font-extrabold tracking-[-0.02em]">{t('new.modpackQuestion')}</h1>
                <p className="mt-1 text-[15px] text-muted-foreground">{t('new.modpackHintPhone')}</p>
              </div>
            ) : phone && from === 'template' ? (
              <div>
                <h1 className="text-[26px] leading-8 font-extrabold tracking-[-0.02em]">{t('new.templateQuestion')}</h1>
                <p className="mt-1 text-[15px] text-muted-foreground">{t('new.templateHint')}</p>
              </div>
            ) : phone ? (
              <>
                <div>
                  <h1 className="text-[26px] leading-8 font-extrabold tracking-[-0.02em]">{t('new.typeQuestion')}</h1>
                  <p className="mt-1 text-[15px] text-muted-foreground">{t('new.typeHint')}</p>
                </div>
                <GameCard phone />
              </>
            ) : (
              <section>
                <h2 className="text-[15px] font-semibold">{t('new.game')}</h2>
                <div className="mt-2.5 grid grid-cols-2 gap-2.5">
                  <GameCard />
                  <div className="flex items-center gap-3 rounded-2xl border border-dashed border-input bg-warm px-4 py-3 text-muted-foreground">
                    <Gamepad2Icon className="size-5" aria-hidden="true" />
                    <span className="text-sm font-semibold text-foreground/80">{t('new.moreGames')}</span>
                  </div>
                </div>
              </section>
            )}
            <section>
              {phone ? (
                <>
                  {startFroms.length > 1 && (
                    <Segmented value={from} onChange={setFrom} options={startFroms.map((f) => ({ value: f.value, label: t(f.short) }))} label={t('new.startFrom')} className="grid w-full grid-cols-3 rounded-xl p-1" itemClassName="h-11 rounded-[10px] text-[15px]" />
                  )}
                  {from === 'type' && (
                    <div className="mt-1 mb-2 flex justify-end">
                      <button type="button" onClick={() => setCompareOpen(true)} className="inline-flex min-h-11 items-center gap-1 text-[15px] font-semibold text-success-strong">
                        {t('new.difference')}
                        <ChevronRightIcon className="size-4" aria-hidden="true" />
                      </button>
                    </div>
                  )}
                </>
              ) : (
                <div>
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <h2 className="text-[15px] font-semibold">{startFroms.length > 1 ? t('new.startFrom') : t('new.from.type')}</h2>
                    {startFroms.length > 1 && <Segmented value={from} onChange={setFrom} options={startFroms.map((f) => ({ value: f.value, label: t(f.long) }))} label={t('new.startFrom')} />}
                  </div>
                  <p className="mt-0.5 text-[13px] text-muted-foreground">
                    {from === 'modpack' ? (
                      t('new.modpackHint')
                    ) : from === 'template' ? (
                      t('new.templateHint')
                    ) : (
                      <>
                        {t('new.typeHint')}{' '}
                        <button type="button" onClick={() => setCompareOpen(true)} className="font-medium text-primary hover:underline">
                          {t('new.difference')}
                        </button>
                      </>
                    )}
                  </p>
                </div>
              )}
              <div key={from} className={cn('animate-fade', phone && from === 'type' ? '' : 'mt-3')}>
                {from === 'modpack' && target ? (
                  <ModpackPicker machineId={target.id} value={pack} onChange={setPack} onUse={startWithPack} phone={phone} />
                ) : from === 'template' && target ? (
                  <TemplatePicker machineId={target.id} value={tpl} onChange={chooseTemplate} handoff={handoff} problem={tplProblem} acceptExperimental={c.acceptExperimental} onAcceptExperimental={(acceptExperimental) => update({ acceptExperimental })} />
                ) : (
                  <TypeCards catalog={catalog} value={c.type} onChange={(type) => type !== c.type && update({ type, versionId: '', build: '', acceptExperimental: false })} phone={phone} />
                )}
              </div>
              <TypeCompare catalog={catalog} open={compareOpen} onOpenChange={setCompareOpen} />
            </section>
          </div>
        )
        break
      case 1: {
        const mods = addonKind(c.type) === 'mods'
        const typeCheck = catalog.types.find((ty) => ty.id === c.type)?.check
        const weakCheck = typeCheck && typeCheck !== 'full' ? typeTexts(c.type)?.check : undefined
        const latest = typeCatalog?.latestRelease
        const notListed = !!latest && !!typeCatalog && typeCatalog.versions.length > 0 && !typeCatalog.versions.some((v) => v.minecraftVersion === latest)
        body = (
          <div className="flex flex-col gap-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('new.versionTitle')}</h2>
                <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('new.versionLead')}</p>
              </div>
              <div className={cn('flex items-center gap-2 text-[13px]', phone ? 'w-full justify-between' : 'rounded-full border border-border bg-muted py-1 pr-3 pl-1.5')}>
                <span className="flex items-center gap-2 font-medium">
                  <TypeLogo type={c.type} size={phone ? 28 : 22} />
                  {t('new.versions', { type: typeName(c.type) })}
                </span>
                <button type="button" onClick={() => go(0)} className="font-semibold text-primary hover:underline max-sm:text-[15px] max-sm:text-success-strong">
                  {t('new.changeType')}
                </button>
              </div>
            </div>
            {typeCatalog?.versionsError && typeCatalog.versions.length === 0 ? (
              <Notice
                tone="error"
                title={t('new.versionsErrorType', { type: typeName(c.type) })}
                action={
                  <Button variant="outline" size="sm" onClick={reload}>
                    <RefreshCwIcon />
                    {t('common.tryAgain')}
                  </Button>
                }
              >
                {typeCatalog.versionsError}
              </Notice>
            ) : typeCatalog ? (
              <VersionPicker catalog={typeCatalog} servers={ws.servers} value={c.versionId} onChange={(versionId) => update({ versionId, build: '', acceptExperimental: false })} acceptExperimental={c.acceptExperimental} onAcceptExperimental={(acceptExperimental) => update({ acceptExperimental })} phone={phone} />
            ) : (
              <CardsSkeleton count={4} className="flex flex-col gap-2.5" card="h-[62px]" />
            )}
            {notListed && <p className="-mt-1 px-0.5 text-xs text-muted-foreground max-sm:text-[13px]">{t('new.notListed', { version: latest, type: typeName(c.type) })}</p>}
            {hasBuilds(c.type) && version && <BuildSelect type={c.type} builds={builds.builds?.builds} loading={builds.loading} error={builds.error} onRetry={builds.reload} value={c.build} onChange={(build) => update({ build })} />}
            {weakCheck && (
              <div>
                <h3 className="text-[13px] font-semibold max-sm:text-[15px]">{t('types.compare.check')}</h3>
                <p className="mt-0.5 text-xs text-muted-foreground max-sm:text-[13px]">{t(weakCheck)}</p>
              </div>
            )}
            {mods ? (
              <p className="text-xs text-muted-foreground max-sm:text-[13px]">{t(c.type === 'neoforge' ? 'new.friendsNeoForge' : 'new.friendsLoader')}</p>
            ) : phone ? (
              <p className="mt-2 text-[13px] text-muted-foreground">{t('new.forwardBody')}</p>
            ) : (
              <div className="grid gap-4 sm:grid-cols-[1fr_1fr]">
                <div />
                <div>
                  <h3 className="text-[13px] font-semibold">{t('new.forward')}</h3>
                  <p className="mt-1 text-xs text-muted-foreground">{t('new.forwardBody')}</p>
                </div>
              </div>
            )}
          </div>
        )
        break
      }
      case 2:
        body = (
          <div className="flex flex-col gap-4">
            <div>
              <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('style.question')}</h2>
              <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('style.lead')}</p>
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
        const packMB = templated ? (tpl?.plan.memoryMB ?? 0) : packed ? (pack?.memoryMB ?? 0) : 0
        const suggested = packMB ? (options.find((mb) => mb >= packMB) ?? options[options.length - 1] ?? 0) : styleMemory(catalog, c.style)
        const largest = options[options.length - 1]
        const others = catalog.servers.filter((x) => !x.running)
        body = (
          <div className="flex flex-col gap-4">
            <div>
              <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('new.memoryTitle')}</h2>
              {!noMemory && (
                <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">
                  {templated && packMB
                    ? t('new.memoryLeadTemplate', { memory: formatMB(suggested) })
                    : packMB && pack
                      ? t('new.memoryLeadPack', { pack: pack.name, memory: formatMB(packMB) })
                      : t('new.memoryLead', { memory: formatMB(suggested), players: budgetAdvice(catalog, suggested)?.players || (preset(c.style)?.players ?? 10) })}
                </p>
              )}
            </div>
            {noMemory ? (
              <Notice tone="warning" title={t('new.noMemoryTitle')}>
                {t('new.noMemory', { machine: machineName })}
              </Notice>
            ) : (
              <>
                <Card className="p-4">
                  <h3 className="text-sm font-semibold">{t('new.machineHas', { machine: machineName, total: formatMB(catalog.hostMemoryMB) })}</h3>
                  <p className="mt-0.5 mb-3 text-xs text-muted-foreground">{others[0] ? t('new.shareHintStopped', { server: others[0].name }) : t('new.shareHint')}</p>
                  <MemoryBar catalog={catalog} memoryMB={c.memoryMB} />
                </Card>
                <Card className="p-4">
                  <h3 className="text-sm font-semibold">{t('new.memoryFor')}</h3>
                  <div className="mt-5 grid items-center gap-6 md:grid-cols-[1fr_200px]">
                    <MemorySlider options={options} value={c.memoryMB} onChange={(memoryMB) => update({ memoryMB })} />
                    <div className="md:border-l md:border-border md:pl-5">
                      <MemoryReadout memoryMB={c.memoryMB} advice={budgetAdvice(catalog, c.memoryMB)} recommended={c.memoryMB === suggested} style={c.style} />
                    </div>
                  </div>
                  {largest !== undefined && (
                    <p className="mt-4 text-xs text-muted-foreground">
                      {catalog.servers.length > 0 ? t('new.maxNote', { memory: formatMB(largest), servers: formatList(catalog.servers.map((x) => x.name)) }) : t('new.maxNoteAlone', { memory: formatMB(largest), machine: machineName })}
                    </p>
                  )}
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
            {!templated && (
              <label className="flex flex-col gap-1.5 text-[13px] font-medium">
                {t('new.motdLabel')}
                <Input value={c.motd} onChange={(e) => update({ motd: e.target.value })} maxLength={59} placeholder={c.name || t('new.motdPlaceholder')} autoComplete="off" className="max-w-[360px] max-sm:max-w-none" />
              </label>
            )}
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

  const note = createNote(step, from, from === 'modpack' ? pack?.type : from === 'template' ? tpl?.plan.type || tpl?.plan.contents.type : c?.type)
  const stepBody = (
    <div key={step} className={cn(stepped && 'animate-page')}>
      {body}
    </div>
  )
  const summary = c && catalog && <Summary choices={c} step={step} port={catalog.suggestedPort} version={version?.minecraftVersion ?? ''} machine={machineName} from={from} pack={from === 'modpack' ? pack : undefined} plan={from === 'template' ? tpl?.plan : undefined} note={note} />
  const tplMods = addonKind(tpl?.plan.type || tpl?.plan.contents.type) === 'mods'
  const continueLabel =
    step === 4 ? t('new.create', { name: c?.name.trim() || t('nav.newServer') }) : step === 0 && from === 'modpack' ? t('new.continuePack') : step === 0 && from === 'template' ? t('new.continueMemory') : phone ? (step === 0 ? t('new.continueVersion') : t('common.continue')) : t(continueKeys[step] ?? 'new.continueName')
  const nextHint = step === 0 && from === 'modpack' ? t('new.nextPack') : step === 0 && from === 'template' ? t(tplMods ? 'new.nextTemplateMods' : 'new.nextTemplate') : step < 4 ? t(nextKeys[step] ?? 'new.nextName', { type: typeName(c?.type) }) : ''
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
              machine={target?.id}
              onPreview={(p) => {
                setRestoreOpen(false)
                setPreview(p)
              }}
            />
          </DialogPanel>
        </DialogPopup>
      </Dialog>
      <RestoreDialog preview={preview} machine={target?.id} onClose={() => setPreview(undefined)} />
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
            <span key={s} className={cn('h-1 flex-1 rounded-full transition-colors duration-(--motion-slow) ease-standard', i <= step ? 'bg-primary' : 'bg-foreground/10')} />
          ))}
        </div>
        {stepBody}
        <div className="mt-6">{restoreLink}</div>
        <PhoneActions>
          <Button size="touch" onClick={next} disabledReason={blocked()} loading={busy}>
            {continueLabel}
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
            {machineName}
            <span className="text-muted-foreground/60" aria-hidden="true">
              /
            </span>
            <span className="font-semibold text-foreground">{t('new.title')}</span>
          </span>
        }
        title={t('new.title')}
        subtitle={t('new.lead', { machine: machineName })}
        actions={
          <>
            {targets.length > 1 && target && (
              <label className="flex items-center gap-2 text-[13px] text-muted-foreground">
                {t('machines.newServerOn')}
                <ChoiceSelect
                  value={target.id}
                  onChange={(id) => navigate({ name: 'new-server', machine: id }, true)}
                  label={t('machines.newServerOn')}
                  options={targets.map((m) => ({ value: m.id, label: m.kind === 'remote' ? machineLabel(m) : ws.machineName }))}
                  className="min-w-36 text-foreground"
                />
              </label>
            )}
            <Button variant="ghost" render={<a {...linkProps({ name: 'home' })} />}>
              {t('new.cancel')}
              <XIcon />
            </Button>
          </>
        }
      />
      <PageBody className="flex flex-col gap-5">
        <Stepper steps={stepTitles} current={step} label={t('new.steps')} />
        <div className="grid gap-6 xl:grid-cols-[1fr_280px]">
          <div className="flex min-w-0 flex-col">
            {stepBody}
            <div className="mt-6 flex items-center gap-3 border-t border-border pt-4">
              {step > 0 && (
                <Button variant="ghost" onClick={back}>
                  <ArrowLeftIcon />
                  {t('common.back')}
                </Button>
              )}
              <span className="ml-auto text-xs text-muted-foreground">{nextHint}</span>
              <Button onClick={next} disabledReason={blocked()} loading={busy}>
                {continueLabel}
                <ArrowRightIcon />
              </Button>
            </div>
            <div className="mt-4">{restoreLink}</div>
          </div>
          <aside className="self-start">
            {summary}
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

function Summary({ choices: c, step, port, version, machine, from, pack, plan, note }: { choices: CreateChoices; step: number; port?: number; version: string; machine: string; from: StartFrom; pack?: ModpackChoice; plan?: TemplatePlan; note?: string }) {
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
  const upNext = <span className="font-semibold text-success-foreground">{t('common.notPicked')}</span>
  const rows: { label: string; value: ReactNode }[] =
    from === 'template'
      ? [
          { label: t('new.row.game'), value: v(true, t('new.gameValue')) },
          { label: t('new.row.startFrom'), value: v(true, t('new.startFrom.template')) },
          { label: t('new.row.type'), value: plan ? v(true, t('new.fromTemplate', { value: typeName(plan.type || plan.contents.type) })) : v(false, '') },
          { label: t('new.row.version'), value: plan ? v(true, t('new.fromTemplate', { value: plan.minecraftVersion || plan.contents.minecraftVersion })) : v(false, '') },
          { label: t('new.row.memory'), value: step >= 3 ? v(step > 3, formatMB(c.memoryMB)) : plan ? upNext : v(false, '') },
          { label: t('new.row.name'), value: step >= 4 ? v(false, c.name) : v(false, '') },
        ]
      : from === 'modpack'
      ? [
          { label: t('new.row.game'), value: v(true, t('new.gameValue')) },
          { label: t('new.row.startFrom'), value: v(true, t('new.startFrom.modpack')) },
          { label: t('new.row.modpack'), value: v(!!pack && step > 0, pack?.name ?? '') },
          { label: t('new.row.type'), value: pack?.type ? v(true, t('new.fromPack', { value: typeName(pack.type) })) : v(false, '') },
          { label: t('new.row.version'), value: pack?.minecraftVersion ? v(true, t('new.fromPack', { value: pack.minecraftVersion })) : v(false, '') },
          { label: t('new.row.memory'), value: step >= 3 ? v(step > 3, formatMB(c.memoryMB)) : v(false, '') },
          ...(step >= 4 ? [{ label: t('new.row.name'), value: v(false, c.name) }] : []),
        ]
      : [
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
          <div className="text-xs text-muted-foreground">{t('new.onMachine', { machine })}</div>
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
      {note && (
        <p key={note} className="mt-4 animate-fade border-t border-border pt-3 text-xs text-muted-foreground">
          {note}
        </p>
      )}
    </Card>
  )
}

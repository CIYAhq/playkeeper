import { useEffect, useId, useState } from 'react'
import { ArrowRightIcon, DownloadIcon, StarIcon, XIcon } from 'lucide-react'
import { Pip } from '@/components/app/art'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { dt } from './messages'

// What the live demo says on its own: once, on the first visit, that the
// data is sample data; and once, after a few of the demo's actions, a quiet
// offer to install it for real. Each is remembered once closed.

const welcomedKey = 'playkeeper-demo-welcomed'
const promptedKey = 'playkeeper-demo-prompted'
const actionEvent = 'playkeeper-demo-action'

/** How many of the demo's actions pass before the quiet prompt. */
export const actionsBeforePrompt = 3

let actions = 0

/** Counts one of the demo's actions; every one of them ends in its toast. */
export function countDemoAction() {
  actions++
  window.dispatchEvent(new Event(actionEvent))
}

function remembered(key: string): boolean {
  try {
    return localStorage.getItem(key) === '1'
  } catch {
    return false
  }
}

function remember(key: string) {
  try {
    localStorage.setItem(key, '1')
  } catch {
    // Storage off: it asks again next time, which is all that's lost.
  }
}

function FirstVisit() {
  const [open, setOpen] = useState(() => !remembered(welcomedKey))
  const close = () => {
    remember(welcomedKey)
    setOpen(false)
  }
  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogPopup className="sm:max-w-[420px]" showCloseButton={false}>
        <div className="flex flex-col items-center px-6 pt-6 pb-6 text-center max-sm:pt-2">
          <Pip pose="wave" size={64} />
          <DialogTitle className="mt-3 text-xl leading-7 font-bold">{dt('demo.welcomeTitle')}</DialogTitle>
          <DialogDescription className="mt-2 text-[15px] leading-[22px]">{dt('demo.welcomeBody')}</DialogDescription>
          <div className="mt-5 grid w-full gap-2.5">
            <Button size="lg" onClick={close}>
              {dt('demo.welcomeStart')}
            </Button>
            <Button size="lg" variant="outline" render={<a href={dt('demo.installUrl')} />}>
              <DownloadIcon />
              {dt('demo.install')}
            </Button>
          </div>
        </div>
      </DialogPopup>
    </Dialog>
  )
}

function QuietPrompt() {
  const [seen, setSeen] = useState(actions)
  const [closed, setClosed] = useState(() => remembered(promptedKey))
  const titleId = useId()
  useEffect(() => {
    const onAction = () => setSeen(actions)
    window.addEventListener(actionEvent, onAction)
    return () => window.removeEventListener(actionEvent, onAction)
  }, [])
  if (closed || seen < actionsBeforePrompt) return null
  const close = () => {
    remember(promptedKey)
    setClosed(true)
  }
  return (
    <aside
      aria-labelledby={titleId}
      className="fixed right-4 bottom-[calc(96px+env(safe-area-inset-bottom))] left-4 z-50 rounded-2xl border border-border bg-card p-4 shadow-popup sm:right-8 sm:bottom-28 sm:left-auto sm:w-[376px]"
    >
      <div className="flex items-start gap-3">
        <Pip pose="wave" size={40} />
        <div className="min-w-0 flex-1">
          <p id={titleId} className="text-[15px] leading-5 font-semibold">
            {dt('demo.promptTitle')}
          </p>
          <p className="mt-0.5 text-[13px] leading-[18px] text-muted-foreground">{dt('demo.cardHint')}</p>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={close} aria-label={dt('demo.promptClose')} className="-mt-1 -mr-1">
          <XIcon />
        </Button>
      </div>
      <div className="mt-3 grid grid-cols-2 gap-2 sm:flex">
        <Button size="sm" className="max-sm:h-10 max-sm:px-2 max-sm:text-[13px]" render={<a href={dt('demo.installUrl')} />}>
          <DownloadIcon />
          {dt('demo.install')}
        </Button>
        <Button size="sm" variant="outline" className="max-sm:h-10 max-sm:px-2 max-sm:text-[13px]" render={<a href={dt('demo.repoUrl')} />}>
          <StarIcon />
          {dt('demo.star')}
        </Button>
      </div>
      <a href={dt('demo.communityUrl')} className="mt-3 inline-flex items-center gap-1 text-[13px] font-medium text-primary hover:underline">
        {dt('demo.ask')}
        <ArrowRightIcon className="size-3.5" aria-hidden="true" />
      </a>
    </aside>
  )
}

/** The demo's own sheet and prompt, over every page. */
export function Overlay() {
  return (
    <>
      <FirstVisit />
      <QuietPrompt />
    </>
  )
}

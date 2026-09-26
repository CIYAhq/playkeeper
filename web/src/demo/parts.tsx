import { DownloadIcon, ExternalLinkIcon } from 'lucide-react'
import { Card, CardHint, CardTitle, CopyButton } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import type { DemoParts } from '@/lib/demo'
import { dt } from './messages'
import { Overlay } from './welcome'

const installCommand = 'curl -fsSL https://playkeeper.io/install | sudo sh'

function BrandLine() {
  return (
    <p className="mt-1 flex items-center gap-1.5 px-1.5 text-xs font-medium text-warning-foreground max-sm:mt-0 max-sm:mb-1 max-sm:px-0">
      <span className="size-1.5 shrink-0 rounded-full bg-warning" aria-hidden="true" />
      {dt('demo.brandLine')}
    </p>
  )
}

function HomeAction() {
  return (
    <Button render={<a href={dt('demo.installUrl')} />}>
      <DownloadIcon />
      {dt('demo.install')}
    </Button>
  )
}

function HomeCard() {
  return (
    <Card>
      <CardTitle>{dt('demo.cardTitle')}</CardTitle>
      <CardHint>{dt('demo.cardHint')}</CardHint>
      <div className="mt-3 rounded-xl bg-console px-3 py-2.5">
        <code className="block font-mono text-xs leading-5 whitespace-pre-wrap text-[#e8e8e0]">{installCommand.replace(' | ', '\n  | ')}</code>
        <CopyButton text={installCommand} aria-label={dt('demo.copyCommand')} className="mt-2 bg-white" />
      </div>
      <a href={dt('demo.installUrl')} target="_blank" rel="noreferrer" className="mt-auto inline-flex items-center gap-1 self-start pt-3 text-xs font-medium text-primary hover:underline">
        {dt('demo.guide')}
        <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
        <span className="sr-only">{dt('demo.newTab')}</span>
      </a>
    </Card>
  )
}

/** The sample world: a file with a world's name and size, and no bytes, which the demo's upload never sends. */
function sampleWorldFile(): File {
  const file = new File([], dt('demo.sampleWorldFile'), { type: 'application/zip' })
  Object.defineProperty(file, 'size', { value: 412 * 1024 * 1024 })
  return file
}

function SampleWorld({ onPick }: { onPick: (files: File[]) => void }) {
  return (
    <p className="mt-2 text-xs text-muted-foreground max-sm:text-[13px]">
      {dt('demo.noWorldFile')}{' '}
      <button type="button" className="font-semibold text-primary hover:underline" onClick={() => onPick([sampleWorldFile()])}>
        {dt('demo.sampleWorld')}
      </button>
    </p>
  )
}

/** What the live demo adds to the dashboard; the demo build puts this in place of lib/demo. */
export const demo: DemoParts = { BrandLine, homeSubtitle: () => dt('demo.subtitle'), HomeAction, HomeCard, templates: false, SampleWorld, Overlay }

import { interpolate, type Vars } from '@/i18n'

// The live demo's own words. They live here rather than in i18n/en.ts so the
// normal build carries none of them; translate them alongside en.ts.
const en = {
  'demo.brandLine': 'Live demo · resets every hour',
  'demo.subtitle': 'Click around. Nothing here is real.',
  'demo.install': 'Install on your VPS',
  'demo.installUrl': '/#install',
  'demo.cardTitle': 'Like what you see?',
  'demo.cardHint': 'About five minutes on a VPS you own.',
  'demo.copyCommand': 'Copy the install command',
  'demo.guide': 'Read the install guide',
  'demo.newTab': '(opens in a new tab)',
  'demo.start': 'That was a demo start',
  'demo.startBody': 'Nothing really started.',
  'demo.stop': 'That was a demo stop',
  'demo.stopBody': 'Nothing really stopped.',
  'demo.restart': 'That was a demo restart',
  'demo.restartBody': 'Nothing really restarted.',
  'demo.backup': 'That was a demo backup',
  'demo.backupBody': 'Nothing was really backed up.',
  'demo.restore': 'That was a demo restore',
  'demo.restoreBody': 'Nothing was really restored.',
  'demo.version': 'That was a demo update',
  'demo.versionBody': 'Nothing was really updated.',
  'demo.create': 'That was a demo server',
  'demo.createBody': 'Nothing was really set up.',
  'demo.delete': 'That was a demo delete',
  'demo.deleteBody': 'Nothing was really deleted.',
  'demo.download': 'That was a demo download',
  'demo.downloadBody': 'There’s no world here to download.',
  'demo.signOut': 'That was a demo sign-out',
  'demo.signOutBody': 'There’s no account here to sign out of.',
  'demo.noData': 'The demo has no sample data for this.',
  'demo.notHere': 'The demo can’t do this one. On your own VPS it works.',
  'demo.noUploads': 'The demo doesn’t take uploads.',
  'demo.noJoin': 'The demo has just this one machine. To connect another, install Playkeeper on a VPS of your own first.',
  'demo.busy': 'One thing at a time: {what}.',
  'demo.taken': 'There’s already a server called {name}.',
  'demo.noMemory': 'There isn’t enough memory left on {machine} for that.',
  'demo.commands': 'The demo knows list, say, time, weather, tps, seed and help.',
}

export type DemoKey = keyof typeof en

/** The demo's text for a key, with {placeholders} filled from vars. */
export function dt(key: DemoKey, vars?: Vars): string {
  return interpolate(en[key], vars)
}

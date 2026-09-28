// The pictures render.mjs and pack-art.mjs make. The site takes one 16:10
// capture per template, templates/<id>.png in a captures folder, which
// site/tools/shots.py writes at the widths the directory's cards and pages
// use (site/static/shots/templates). --extra folders also get a 16:9 and a
// square picture of the same view, for sharing elsewhere.
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export const SITE = { name: '16x10', w: 1920, h: 1200 }
export const EXTRA = [{ name: '16x9', w: 1920, h: 1080 }, { name: '1x1', w: 1200, h: 1200 }]

// save writes a template's picture in one shape: the site's into
// <captures>/templates/<id>.png, an extra one into <extra>/<id>-<shape>.png.
export function save ({ captures, extra }, id, shape, png) {
  if (shape === SITE.name) {
    mkdirSync(join(captures, 'templates'), { recursive: true })
    writeFileSync(join(captures, 'templates', `${id}.png`), png)
  } else if (extra) {
    mkdirSync(extra, { recursive: true })
    writeFileSync(join(extra, `${id}-${shape}.png`), png)
  }
}

// shapes are the pictures to make: the site's, and the extra ones when
// there's somewhere to put them.
export function shapes ({ extra }) {
  return extra ? [SITE, ...EXTRA] : [SITE]
}

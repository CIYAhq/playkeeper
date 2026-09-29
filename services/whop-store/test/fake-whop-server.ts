// The make-believe Whop API over HTTP, for running the built store against:
// node test/fake-whop-server.ts [port]. The first part of the path picks the
// business, so each store reads its own with WHOP_API_ORIGIN set to
// http://127.0.0.1:<port>/open (Pip Hosting, taking orders) or /fresh (a
// fresh copy of the blueprint).
import { createServer } from 'node:http'
import { fakeWhop, freshCopy, openStore, type Catalogue } from './fake-whop.ts'

const port = Number(process.argv[2] ?? 4190)
const stores: Record<string, () => Catalogue> = { open: openStore, fresh: freshCopy }

createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', `http://127.0.0.1:${port}`)
  const [, name = '', ...rest] = url.pathname.split('/')
  const catalogue = stores[name]
  if (url.pathname === '/healthz') {
    res.writeHead(200, { 'content-type': 'text/plain' }).end('ok')
    return
  }
  if (!catalogue) {
    res.writeHead(404, { 'content-type': 'application/json' }).end(JSON.stringify({ error: { type: 'not_found', message: 'No such store' } }))
    return
  }
  const answer = await fakeWhop(catalogue).fetch(`https://api.whop.com/${rest.join('/')}${url.search}`, { headers: req.headers as Record<string, string> })
  res.writeHead(answer.status, { 'content-type': 'application/json' }).end(await answer.text())
}).listen(port, '127.0.0.1', () => console.log(`fake Whop API on http://127.0.0.1:${port}`))

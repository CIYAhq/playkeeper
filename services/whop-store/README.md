# The Whop store site

This folder is the storefront a Minecraft hosting business runs on Whop: a small site Whop hosts at `<route>.whop.site`, showing the plans of the Playkeeper that sells them, with Whop's checkout. It's the site of the Pip Hosting blueprint, which anyone can copy from Whop's gallery to start a hosting business on their own VPS. It is not part of the Playkeeper release.

Whop runs it as a Cloudflare Worker. Cloudflare's Vite plugin builds it into `dist/client` (the stylesheet, favicon and `robots.txt`, served as they are) and `dist/server/index.js` (the Worker), and Whop's `whop()` plugin packs both into `dist/whop-build.zip`, which `whop apps deploy` uploads.

## What it shows

It reads the business, its products and its plans from Whop's API. On Whop, requests go through Whop hosting's own proxy, which adds the app's API key, so no key is in the code or its settings. It pins Whop's API version, keeps each read for a minute, and keeps showing the last one for an hour while Whop can't be reached.

- **The plans on sale:** each visible plan of a product that a connected Playkeeper has marked with its address (`playkeeper_dashboard`, which **Settings › Sell on Whop** sets), cheapest first. A plan shows its servers and memory from its metadata (`playkeeper_servers`, `playkeeper_memory_gb`), its free trial, and Whop's checkout link. Sold-out and waitlist plans say so.
- **Not taking orders yet:** a fresh copy of the blueprint, whose Playkeeper isn't connected, or a store whose plans are all hidden. The page tells a seller how to open it.
- **Sign in:** a link to the Playkeeper that sells the plans, for customers.
- **Pages:** the store with how it works and questions, terms (or the business's own terms from Whop), a 404, and a page for when Whop can't be reached. It carries the "not an official Minecraft service" line and no Minecraft logo or art.
- **Safety:** everything from Whop is escaped. Checkout links must be `https` links on `whop.com`, and the dashboard, logo and terms links must be `https`.

## Work on it

With Node 24 (`scripts/setup.sh` installs the pinned one):

```sh
npm ci
npm run typecheck
npm test          # the Worker's code in Node, against a fake Whop API
npm run build     # dist/, and dist/whop-build.zip
```

To run the build as Whop does, in workerd, against the fake Whop API:

```sh
node test/fake-whop-server.ts 4190 &
npx wrangler dev --config dist/server/wrangler.json --port 4191 \
  --var WHOP_API_ORIGIN:http://127.0.0.1:4190/open --var WHOP_ACCOUNT_ID:biz_pip
```

Then open `http://127.0.0.1:4191`. `/fresh` in place of `/open` is a fresh copy of the blueprint. The Worker's module may export nothing but its handler, since the runtime takes every named export for an entrypoint, so the handler lives in `src/handler.ts`.

CI builds it and checks it in a browser at desktop and phone sizes whenever this folder changes (the `whop-store` part of `.github/workflows/e2e.yml`, with `test/e2e/ui/whop-store.spec.ts`).

## Deploy it

On a machine where the Whop CLI is signed in to the business, from this folder:

```sh
whop apps deploy --app app_xxxxxxxx            # build, upload, go live
whop apps deploy --app app_xxxxxxxx --preview  # upload without going live
```

Each deploy also uploads this folder's source, which is what a copy of the blueprint gets when its seller clones it (`whop apps init --template`). The site stays out of Whop's gallery until its app is published.

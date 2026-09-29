import { createHandler } from './handler.ts'
import type { Env } from './whop.ts'

// The Worker Whop hosting runs. The runtime takes each named export of this
// module for an entrypoint, so it exports nothing but the handler.
const handle = createHandler()

export default {
  fetch(request: Request, env: Env) {
    return handle(request, env)
  },
}

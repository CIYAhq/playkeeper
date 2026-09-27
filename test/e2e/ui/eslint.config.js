// The browser tests follow the web UI's lint rules; `make lint` runs both.
import web from '../../../web/eslint.config.js'

export default [{ ignores: ['node_modules', 'test-results', 'playwright-report'] }, ...web]

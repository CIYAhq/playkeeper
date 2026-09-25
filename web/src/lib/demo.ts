import type { ComponentType } from 'react'

/** What the live demo adds to the dashboard. */
export interface DemoParts {
  /** The amber line under the brand, or at the top of a phone's page. */
  BrandLine: ComponentType
  /** Home's line under its title. */
  homeSubtitle: () => string
  /** Home's main action, in place of New server. */
  HomeAction: ComponentType
  /** The card after the server cards, in place of the New server card. */
  HomeCard: ComponentType
}

/**
 * The demo build (`vite build --mode demo`) swaps this module for
 * src/demo/parts.tsx; everywhere else there is no demo.
 */
export const demo: DemoParts | undefined = undefined

import type { Page } from '@playwright/test'
import type { MockModelRateLimits } from './mockModelScript'
import { expect } from '@playwright/test'
import { rateLimitPopoverLabel } from '../../../src/lib/rateLimitUtils'
import { openAgentInfoCard, visibleOnly } from './ui'

/**
 * Rate-limit state on screen.
 *
 * Two paths reach it. A model step can script a `rateLimits` block, whose
 * headers a CLI forwards until the agent info card shows the window. And a
 * provider can answer a call with 429, whose reason the transcript states in
 * the provider's own words.
 */

/**
 * The card's heading for one window type.
 *
 * The label table is the app's own (`rateLimitPopoverLabel`), and the fallback
 * is the app's own too, so a wire value this build does not know reads the same
 * here and on the card.
 */
export function rateLimitWindowLabel(type: string | undefined): string {
  return rateLimitPopoverLabel(type) ?? (type ? `Rate Limit (${type})` : 'Rate Limit')
}

/**
 * The text markers the agent info card must show for one scripted rate-limit
 * window.
 *
 * The heading comes first, then the utilization. The card prints the
 * utilization as a whole-percent figure ("92% used"), not the raw fraction the
 * step scripted, so the marker is that phrase.
 */
export function rateLimitMarkers(rateLimits: MockModelRateLimits): string[] {
  const markers = [rateLimitWindowLabel(rateLimits.type)]
  if (rateLimits.utilization !== undefined)
    markers.push(`${Math.round(rateLimits.utilization * 100)}% used`)
  return markers
}

/**
 * Open the agent info card and assert its rate-limit rows follow `rateLimits`.
 */
export async function expectRateLimitWindow(page: Page, rateLimits: MockModelRateLimits): Promise<void> {
  const popover = await openAgentInfoCard(page)
  for (const marker of rateLimitMarkers(rateLimits))
    await expect(popover).toContainText(marker)
}

/**
 * Assert the transcript states a rate-limit notice after a refused call.
 *
 * The wording is the provider's own, so the caller passes it. A page-rooted text
 * match also finds ChatView's hidden premeasure copy, so the locator filters to
 * what the user sees and takes the first hit.
 */
export async function expectRateLimitNotice(page: Page, text: string | RegExp): Promise<void> {
  await expect(visibleOnly(page.getByText(text, { exact: false })).first()).toBeVisible()
}

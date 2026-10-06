import type { Page } from '@playwright/test'
import type { MockModelRateLimits, MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { rateLimitPopoverLabel } from '../../../src/lib/rateLimitUtils'
import { nativeTextStep } from './nativeScenario'
import { uniqueMarker } from './shellArguments'
import { assistantBubbles, openAgentInfoCard, sendMessage, visibleOnly, waitForAgentIdle } from './ui'

/**
 * Rate-limit state on screen.
 *
 * Two paths reach it. A model step can script a `rateLimits` block, whose
 * headers a CLI forwards until the agent info card shows the window. And a
 * provider can answer a call with 429, whose reason the transcript states in
 * the provider's own words.
 */

/** The utilization of a window near its limit, which a native client reports as a warning. */
export const NEAR_LIMIT_UTILIZATION = 0.92

/**
 * A rate-limit window near its limit, which resets one hour from now.
 * The warning status is the case where a provider that supports quota state shows a window, so a negative proof
 * with this input is the strongest.
 */
export function nearLimitRateLimits(type = 'five_hour'): MockModelRateLimits {
  if (type.trim() === '')
    throw new Error('A rate-limit window needs a type.')
  return { type, status: 'allowed_warning', utilization: NEAR_LIMIT_UTILIZATION, resetsAt: Math.floor(Date.now() / 1000) + 3600 }
}

/**
 * Run one native turn whose model response carries `rateLimits`, and require the window on the agent info card.
 * With `reload`, require the window again after a reload, which reads the stored state.
 * Return the model request of the turn. The mock adds the response receipt when the response ends, so the function
 * reads the record after the turn.
 */
export async function exerciseRateLimitWindow(
  context: NativeScenarioContext,
  rateLimits: MockModelRateLimits,
  options: { reload?: boolean } = {},
): Promise<MockModelRequestRecord> {
  const answer = uniqueMarker('RATELIMITWINDOW')
  const start = await context.modelScript.queue({ ...nativeTextStep(context, answer), rateLimits })
  await sendMessage(context.page, context.modelScript.prompt('Reply once near the scripted rate limit.'))
  await context.modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(context.page)
  const request = await context.modelScript.requestAt(start)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  await expectRateLimitWindow(context.page, rateLimits)
  if (options.reload) {
    await context.page.reload()
    await expectRateLimitWindow(context.page, rateLimits)
  }
  return request
}

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

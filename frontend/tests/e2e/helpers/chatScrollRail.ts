import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'
import { sendScriptedTurn } from './scriptedTurn'
import { userBubbles, waitForAgentStarted } from './ui'

/**
 * The chat scroll rail as the E2E specs reach it: its locators, and the conversation that makes it show.
 * `047-chat-scroll-rail.spec.ts` covers the geometry, the dots, and the seek of the rail,
 * `047b-chat-scroll-rail-autohide.spec.ts` its auto-hide, and `047c-chat-scroll-rail-scrub.spec.ts` its scrub.
 */

export const RAIL = '[data-testid="chat-scroll-rail"]'
export const THUMB = '[data-testid="chat-scroll-rail-thumb"]'
export const PREVIEW = '[data-testid="chat-scroll-rail-preview"]'
export const RAIL_DOT = '[data-testid="chat-scroll-rail-dot"]'

/** The start of {@link RAIL_FILLER_MESSAGE}, which the preview card of its dot shows. */
export const RAIL_FILLER_PREVIEW = 'Please just reply with "ok"'

/**
 * A message that is long enough that its user bubble is tall: a short viewport then overflows, and the rail shows.
 * Without the overflow the rail correctly hides itself.
 *
 * The prompt is the runtime of the test, so make the VIEWPORT shorter, never the message longer, when a test needs
 * more overflow. A 2.5 times longer filler once pushed the turn past the idle wait under parallel load.
 */
export const RAIL_FILLER_MESSAGE = `${RAIL_FILLER_PREVIEW}. Ignore this filler: ${'the quick brown fox jumps over the lazy dog. '.repeat(12)}`

/**
 * Send `messages` tall messages, one scripted turn each, so the conversation overflows and the rail takes over the
 * scroll. Require the rail before the return: without the overflow every later step of the caller would fail on a
 * missing element, not on "the viewport was too tall for the messages".
 *
 * The agent must end its startup first, so the send takes the fast path.
 */
export async function seedOverflowingConversation(page: Page, script: ModelScript, messages = 1): Promise<void> {
  if (!Number.isSafeInteger(messages) || messages < 1)
    throw new RangeError(`A conversation that overflows needs at least one message, not ${messages}.`)
  await waitForAgentStarted(page)
  for (let message = 0; message < messages; message++)
    await sendScriptedTurn(page, script, { prompt: RAIL_FILLER_MESSAGE, answer: 'ok' })
  await expect(userBubbles(page)).toHaveCount(messages)
  await expect(page.locator(RAIL), 'the conversation overflows, so the rail shows').toBeVisible()
}

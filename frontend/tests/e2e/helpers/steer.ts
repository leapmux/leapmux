import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { assistantBubbles, sendMessage } from './ui'

/**
 * Steering a running turn.
 *
 * A message sent while a turn runs waits in LeapMux's input queue. The queue
 * offers to steer with it, and the Steer button sends it into the running turn
 * as a steering line. The row leaves the queue once the worker sent the steer,
 * and the reply the steered turn ends on lands in the transcript.
 */

/**
 * The test-id pattern of a queued-input row.
 *
 * ANCHORED on purpose. The row test id is `queued-input-<id>`, and an
 * unanchored pattern also matches a longer id that merely contains the words.
 */
export const QUEUED_INPUT_ROW_TESTID = /^queued-input-/

/** The Steer button of one queued-input row. */
export const STEER_BUTTON_NAME = 'Steer'

/** One message to steer into a running turn. */
export interface SteerInput {
  /** The message sent while the turn runs. */
  message: string
  /**
   * A distinctive fragment of `message` that finds its queued row.
   *
   * The row shows a preview that need not carry the whole message, so the
   * specs match a stable prefix rather than the full text.
   */
  match: string
}

/** The queued-input row whose preview contains `match`. */
export function queuedInputRow(page: Page, match: string): Locator {
  return page.getByTestId(QUEUED_INPUT_ROW_TESTID).filter({ hasText: match })
}

/** The Steer button on one queued-input row. */
export function steerButton(row: Locator): Locator {
  return row.getByRole('button', { name: STEER_BUTTON_NAME })
}

/**
 * Queue `input.message` during a running turn and steer it into that turn.
 *
 * The row leaves the queue once the worker sent the steer.
 */
export async function steerQueuedInput(page: Page, input: SteerInput): Promise<void> {
  await sendMessage(page, input.message)
  const queued = queuedInputRow(page, input.match)
  await expect(queued).toBeVisible()
  const steer = steerButton(queued)
  await expect(steer).toBeVisible()
  await steer.click()
  await expect(queued).toHaveCount(0)
}

/**
 * Assert the steered reply landed in an assistant bubble.
 *
 * `at` picks the bubble when several match: the last one when an earlier
 * bubble already carries the words, the first one otherwise.
 */
export async function expectSteeredReply(page: Page, reply: string, at: 'first' | 'last'): Promise<void> {
  const replies = assistantBubbles(page).filter({ hasText: reply })
  await expect(at === 'first' ? replies.first() : replies.last()).toBeVisible()
}

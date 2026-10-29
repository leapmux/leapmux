import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { QUEUED_INPUT_ROW_TESTID } from './steer'
import { inputQueue, queuePauseButton } from './ui'

/**
 * The two kinds of automatic retry that the input queue holds, as the row label states them.
 * `OPERATION_LABELS` in `src/components/chat/AgentInputQueue.tsx` writes the label as `Auto-retry (<kind>)`.
 */
export type AutoRetryKind = 'usage limit' | 'server error'

/** The label of an automatic-retry row of one kind. */
export function autoRetryLabel(kind: AutoRetryKind): string {
  return `Auto-retry (${kind})`
}

/** Locate the automatic-retry rows of one kind in the visible input queue. */
export function autoRetryRows(page: Page, kind: AutoRetryKind): Locator {
  return inputQueue(page).getByTestId(QUEUED_INPUT_ROW_TESTID).filter({
    hasText: autoRetryLabel(kind),
  })
}

/**
 * Require exactly one automatic-retry row of a kind, delete it, and wait until it is gone.
 * A retry that matured would send a model request that no step scripts, and that request fails the spec.
 *
 * The row is selected by its own test id before the clicks. Delete takes a confirmation click, and the row stays
 * until the Worker returns its snapshot, so a second lookup through the filter could select a row that is going.
 */
export async function deleteAutoRetryRow(page: Page, kind: AutoRetryKind): Promise<void> {
  const rows = autoRetryRows(page, kind)
  await expect(rows).toHaveCount(1)
  const rowId = await rows.getAttribute('data-testid')
  if (!rowId)
    throw new Error(`The ${autoRetryLabel(kind)} row has no test id.`)
  const row = inputQueue(page).getByTestId(rowId)
  await row.getByRole('button', { name: 'Delete', exact: true }).click()
  await row.getByRole('button', { name: 'Confirm delete?', exact: true }).click()
  await expect(row).toHaveCount(0)
}

/**
 * Pause the input queue unless it is paused already, and report whether this call paused it.
 * The pause button toggles, so a click on a paused queue would resume it: the label decides.
 * When the Worker pauses the queue between the read and the click, the click resumes it, and the check of the
 * new label then fails the spec instead of letting it continue on a running queue.
 */
export async function pauseQueueUnlessPaused(page: Page): Promise<boolean> {
  const button = queuePauseButton(page)
  if ((await button.textContent())?.trim() !== 'Pause Queue')
    return false
  await button.click()
  await expect(button).toHaveText('Resume Queue')
  return true
}

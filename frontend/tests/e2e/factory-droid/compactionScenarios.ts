import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '../droid-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseCompletedManualCompaction(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const oldMarker = 'DROID_OLD_CONTEXT_MARKER'
  const summaryMarker = 'DROID_COMPACT_SUMMARY_MARKER'
  const olderAnswer = Array.from({ length: 300 }, (_, index) => `${oldMarker} item ${index}: detail ${index * 7}.`).join(' ')
  for (let turn = 0; turn < 4; turn++) {
    const step = await modelScript.queue({ text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.` })
    await sendMessage(page, modelScript.prompt(`Record Droid task turn ${turn}.`))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
    if (turn === 3)
      expect(JSON.stringify((await modelScript.requestAt(step)).body)).toContain(oldMarker)
  }
  await expect(assistantBubbles(page).filter({ hasText: 'Recent task answer 3.' })).toBeVisible()

  await modelScript.fallback({ text: `${summaryMarker} Preserve the current task.` })
  await sendMessage(page, '/compact')
  await expectCompactionNotice(page)
  await waitForAgentIdle(page)
  expect((await modelScript.status()).requests.some(request => request.fallback)).toBe(true)
  await expect(messageBubbles(page).filter({ hasText: 'settings_updated' })).toHaveCount(0)
  await expect(messageBubbles(page).filter({ hasText: '"type":"session_compacted"' })).toHaveCount(0)
  await page.reload()
  await expectCompactionNotice(page)
  await expect(messageBubbles(page).filter({ hasText: 'settings_updated' })).toHaveCount(0)

  const next = await modelScript.queue({ text: 'The compacted Droid session continued.' })
  await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const nextBody = JSON.stringify((await modelScript.requestAt(next)).body)
  expect(nextBody).toContain(summaryMarker)
  expect(nextBody).not.toContain(oldMarker)
}

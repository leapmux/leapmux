import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '../codebuddy-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseContextCompactionWithoutNotice(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const oldMarker = 'CODEBUDDY_OLD_CONTEXT_MARKER'
  const summaryMarker = 'CODEBUDDY_COMPACT_SUMMARY_MARKER'
  const olderAnswer = Array.from({ length: 300 }, (_, index) => `${oldMarker} item ${index}: detail ${index * 7}.`).join(' ')
  for (let turn = 0; turn < 4; turn++) {
    const step = await modelScript.queue({ text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.` })
    await sendMessage(page, modelScript.prompt(`Record CodeBuddy task turn ${turn}.`))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
    if (turn === 3)
      expect(JSON.stringify((await modelScript.requestAt(step)).body)).toContain(oldMarker)
  }
  await expect(assistantBubbles(page).filter({ hasText: 'Recent task answer 3.' })).toBeVisible()

  // CodeBuddy rejects a compaction reply without this native XML envelope.
  const summaryStep = await modelScript.queue({
    text: `<conversation_history_summary><summary>${summaryMarker} Preserve the current task.</summary></conversation_history_summary>`,
  })
  await sendMessage(page, '/compact')
  await modelScript.waitForSteps(summaryStep + 1)
  await waitForAgentIdle(page)
  expect(JSON.stringify((await modelScript.requestAt(summaryStep)).body)).toContain('"agent":"compact"')
  await expect(compactionNoticeRow(page)).toHaveCount(0)

  const next = await modelScript.queue({ text: 'The compacted CodeBuddy session continued.' })
  await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const nextBody = JSON.stringify((await modelScript.requestAt(next)).body)
  expect(nextBody).toContain(summaryMarker)
  expect(nextBody).not.toContain(oldMarker)
}

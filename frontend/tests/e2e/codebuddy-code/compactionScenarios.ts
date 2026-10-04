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
    await modelScript.queue({ text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.` })
    await sendMessage(page, modelScript.prompt(`Record CodeBuddy task turn ${turn}.`))
    const prior = await modelScript.waitForSteps(turn + 1)
    await waitForAgentIdle(page, 180_000)
    if (turn === 3)
      expect(JSON.stringify(prior.requests.find(request => request.stepIndex === turn)?.body)).toContain(oldMarker)
  }
  await expect(assistantBubbles(page).filter({ hasText: 'Recent task answer 3.' })).toBeVisible()

  // CodeBuddy rejects a compaction reply without this native XML envelope.
  await modelScript.queue({
    text: `<conversation_history_summary><summary>${summaryMarker} Preserve the current task.</summary></conversation_history_summary>`,
  })
  await sendMessage(page, '/compact')
  const status = await modelScript.waitForSteps(5)
  await waitForAgentIdle(page, 180_000)
  const nativeRequest = status.requests.find(request => request.stepIndex === 4)
  expect(JSON.stringify(nativeRequest?.body)).toContain('"agent":"compact"')
  await expect(compactionNoticeRow(page)).toHaveCount(0)

  await modelScript.queue({ text: 'The compacted CodeBuddy session continued.' })
  await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
  const continued = await modelScript.waitForSteps(6)
  await waitForAgentIdle(page, 180_000)
  const nextBody = JSON.stringify(continued.requests.find(request => request.stepIndex === 5)?.body)
  expect(nextBody).toContain(summaryMarker)
  expect(nextBody).not.toContain(oldMarker)
}

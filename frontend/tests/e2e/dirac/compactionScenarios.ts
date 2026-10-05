import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '../dirac-fixtures'
import { diracCondenseToolCall, diracRespondToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseNativeCondense(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const oldMarker = 'DIRAC_OLD_CONTEXT_MARKER'
  const marker = 'DIRAC_COMPACT_MARKER Preserve the branch state.'
  for (const [index, prompt] of [
    'Start a baseline task.',
    `${oldMarker} is old work that the summary will replace.`,
    'Finish another task before compaction.',
  ].entries()) {
    await modelScript.queue({ toolCalls: [diracRespondToolCall(`dirac-before-smol-${index}`, 'complete', `Turn ${index} ended.`)] })
    await sendMessage(page, modelScript.prompt(prompt))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
  }

  await modelScript.queue(
    { toolCalls: [diracCondenseToolCall('dirac-smol-summary', marker)] },
    { toolCalls: [diracRespondToolCall('dirac-after-smol', 'complete', 'The condensed turn ended.')] },
  )
  await sendMessage(page, modelScript.prompt('/smol'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  expect(JSON.stringify(status.requests.find(record => record.stepIndex === 3)?.body)).toContain('explicit_instructions type=')
  await expect(page.locator('[data-chat-scroll-container="true"]:visible').getByText('Conversation Condensed').first()).toBeVisible()

  await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-compacted-followup', 'complete', 'The next task ended.')] })
  await sendMessage(page, modelScript.prompt('Continue after compaction.'))
  const next = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const nextRequest = next.requests.filter(record => record.stepIndex !== undefined).at(-1)
  expect(JSON.stringify(nextRequest?.body)).toContain(marker)
  expect(JSON.stringify(nextRequest?.body)).not.toContain(oldMarker)
}

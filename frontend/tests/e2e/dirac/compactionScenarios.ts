import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '../dirac-fixtures'
import { diracCondenseToolCall, diracRespondToolCall } from '../helpers/providerToolCalls'
import { chatScrollContainer, sendMessage, waitForAgentIdle } from '../helpers/ui'

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
    const step = await modelScript.queue({ toolCalls: [diracRespondToolCall(`dirac-before-smol-${index}`, 'complete', `Turn ${index} ended.`)] })
    await sendMessage(page, modelScript.prompt(prompt))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
  }

  const condense = await modelScript.queue(
    { toolCalls: [diracCondenseToolCall('dirac-smol-summary', marker)] },
    { toolCalls: [diracRespondToolCall('dirac-after-smol', 'complete', 'The condensed turn ended.')] },
  )
  await sendMessage(page, modelScript.prompt('/smol'))
  await modelScript.waitForSteps(condense + 2)
  await waitForAgentIdle(page)
  expect(JSON.stringify((await modelScript.requestAt(condense)).body)).toContain('explicit_instructions type=')
  await expect(chatScrollContainer(page).getByText('Conversation Condensed').first()).toBeVisible()

  const next = await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-compacted-followup', 'complete', 'The next task ended.')] })
  await sendMessage(page, modelScript.prompt('Continue after compaction.'))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const nextBody = JSON.stringify((await modelScript.requestAt(next)).body)
  expect(nextBody).toContain(marker)
  expect(nextBody).not.toContain(oldMarker)
}

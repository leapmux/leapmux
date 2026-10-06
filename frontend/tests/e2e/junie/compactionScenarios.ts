import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseCompressAcknowledgement(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  const oldMarker = 'JUNIEOLDCONTEXTMARKER'
  const priorAnswerMarker = 'JUNIEPRIORANSWERMARKER'
  let lastBaseline = -1
  for (const [index, prompt] of [
    'Start a baseline task.',
    `${oldMarker} is old work to replace.`,
    'Finish another task before compaction.',
  ].entries()) {
    const answer = index === 0 ? `Turn ${index} is complete. Keep ${priorAnswerMarker} as task state.` : `Turn ${index} is complete.`
    lastBaseline = await modelScript.queue({ toolCalls: [junieAnswerToolCall(`junie-before-compress-${index}`, answer)] })
    await sendMessage(page, modelScript.prompt(prompt))
    await modelScript.waitForSteps(lastBaseline + 1)
    await waitForAgentIdle(page)
  }

  expect(JSON.stringify((await modelScript.requestAt(lastBaseline)).body)).toContain(oldMarker)
  const before = await modelScript.status()

  await sendMessage(page, '/compress')
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Context will be compressed before the next task' }).first()).toBeVisible()
  const afterCommand = await modelScript.status()
  expect(afterCommand.requests).toHaveLength(before.requests.length)
  await expect(compactionNoticeRow(page)).toHaveCount(0)

  const next = await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-after-compress', 'The compressed task ended.')] })
  await sendMessage(page, modelScript.prompt('JUNIE_NEXT_TASK_MARKER Continue after compression.'))
  await modelScript.waitForSteps(next + 1)
  await waitForAgentIdle(page)
  const nextBody = JSON.stringify((await modelScript.requestAt(next)).body)
  expect(nextBody).toContain('History processor: During the current session')
  const previousSolutions = [...nextBody.matchAll(/<previous_issue_solution>[\s\S]*?<\/previous_issue_solution>/g)]
    .map(match => match[0])
  expect(previousSolutions.some(solution => solution.includes(priorAnswerMarker))).toBe(true)

  const later = await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-later-task', 'The later task ended.')] })
  await sendMessage(page, modelScript.prompt('Continue one more task after compression.'))
  await modelScript.waitForSteps(later + 1)
  await waitForAgentIdle(page)
  const laterBody = JSON.stringify((await modelScript.requestAt(later)).body)
  expect(nextBody.includes(oldMarker)).toBe(true)
  expect(laterBody.includes(oldMarker)).toBe(true)
  expect(laterBody.includes(priorAnswerMarker)).toBe(true)
}

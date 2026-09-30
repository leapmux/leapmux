import { compactionNoticeRow } from './helpers/compaction'
import { junieAnswerToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

junieTest('keeps prior context after the native compress acknowledgement', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
  void authenticatedJunieWorkspace
  const oldMarker = 'JUNIEOLDCONTEXTMARKER'
  const priorAnswerMarker = 'JUNIEPRIORANSWERMARKER'
  await modelScript.rule(
    { name: 'junie-compress-capability', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-compress-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Compaction task' } },
  )
  for (const [index, prompt] of [
    'Start a baseline task.',
    `${oldMarker} is old work to replace.`,
    'Finish another task before compaction.',
  ].entries()) {
    const answer = index === 0 ? `Turn ${index} is complete. Keep ${priorAnswerMarker} as task state.` : `Turn ${index} is complete.`
    await modelScript.queue({ toolCalls: [junieAnswerToolCall(`junie-before-compress-${index}`, answer)] })
    await sendMessage(page, modelScript.prompt(prompt))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
  }

  const before = await modelScript.status()
  expect(JSON.stringify(before.requests.find(request => request.stepIndex === 2)?.body)).toContain(oldMarker)

  await sendMessage(page, '/compress')
  await waitForAgentIdle(page, 120_000)
  await expect(assistantBubbles(page).filter({ hasText: 'Context will be compressed before the next task' }).first()).toBeVisible()
  const afterCommand = await modelScript.status()
  expect(afterCommand.requests).toHaveLength(before.requests.length)
  await expect(compactionNoticeRow(page)).toHaveCount(0)

  await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-after-compress', 'The compressed task ended.')] })
  await sendMessage(page, modelScript.prompt('JUNIE_NEXT_TASK_MARKER Continue after compression.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 120_000)
  const nextRequest = status.requests.find(request => request.stepIndex === 3)
  const nextBody = JSON.stringify(nextRequest?.body)
  expect(nextBody).toContain('History processor: During the current session')
  const previousSolutions = [...nextBody.matchAll(/<previous_issue_solution>[\s\S]*?<\/previous_issue_solution>/g)]
    .map(match => match[0])
  expect(previousSolutions.some(solution => solution.includes(priorAnswerMarker))).toBe(true)

  await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-later-task', 'The later task ended.')] })
  await sendMessage(page, modelScript.prompt('Continue one more task after compression.'))
  const laterStatus = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 120_000)
  const laterBody = JSON.stringify(laterStatus.requests.find(request => request.stepIndex === 4)?.body)
  expect(nextBody.includes(oldMarker)).toBe(true)
  expect(laterBody.includes(oldMarker)).toBe(true)
  expect(laterBody.includes(priorAnswerMarker)).toBe(true)
})

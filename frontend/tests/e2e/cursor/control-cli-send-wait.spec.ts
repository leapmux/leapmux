import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

function sendAndWait(id: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    id,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
}

// Cursor's Run exchange answers its own question in the same scripted step and
// emits its own selection summary rather than taking a second model request.
cursorTest('retries the exact reply while a later native question is open', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Report the release checklist status.'
  const answer = 'The release checklist passed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = sendAndWait(id, modelScript.prompt(prompt), 'cursor-release-checklist')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    input_id: expect.any(String),
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'cursor-cli-question', [NATIVE_CONTROL_QUESTION]),
    ],
  })
  const second = sendAndWait(id, modelScript.prompt('Ask the scripted question.'), 'cursor-release-question')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({
    input_id: expect.any(String),
    state: 'waiting_for_user',
  })
  await expect(await waitForControlBanner(page)).toContainText(NATIVE_CONTROL_QUESTION.question)
  await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  const { requests } = await runCLI(cli, ['agent', 'requests', id]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  await runCLI(cli, [
    'agent',
    'answer',
    id,
    requests[0]!.id,
    '--option',
    '2',
  ])
  const expectedAnswer = 'Cursor question selected: option-1-2'
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [expectedAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: expectedAnswer })).toHaveCount(1)
})

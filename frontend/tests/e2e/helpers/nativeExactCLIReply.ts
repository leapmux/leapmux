import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from './cli'
import { NATIVE_CONTROL_QUESTION } from './nativeQuestion'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { askUserQuestionToolCall } from './providerToolCalls'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from './ui'

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

export async function exerciseExactCLIReply(native: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = native
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Report the release checklist status.'
  const answer = 'The release checklist passed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = sendAndWait(id, modelScript.prompt(prompt), 'release-checklist')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    input_id: expect.any(String),
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextReply = 'I recorded the second option.'
  await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'cli-question', [NATIVE_CONTROL_QUESTION]),
    ],
  }, nativeTextStep(native, nextReply))
  const second = sendAndWait(id, modelScript.prompt('Ask the scripted question.'), 'release-question')
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
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextReply],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextReply })).toHaveCount(1)
}

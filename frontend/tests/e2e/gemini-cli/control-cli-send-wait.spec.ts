import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
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

geminiTest('retries the exact reply while a later native permission is open', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Report the release checklist status.'
  const answer = 'The release checklist passed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = sendAndWait(id, modelScript.prompt(prompt), 'gemini-release-checklist')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    input_id: expect.any(String),
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextReply = 'The approved Gemini shell check completed.'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'gemini-cli-permission', 'printf gemini-permission-proof'),
    ],
  }, nativeTextStep(native, nextReply))
  const second = sendAndWait(id, modelScript.prompt('Run the scripted shell check.'), 'gemini-release-permission')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({
    input_id: expect.any(String),
    state: 'waiting_for_user',
  })
  await waitForControlBanner(page)
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
    'approve',
    id,
    requests[0]!.id,
  ])
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextReply],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextReply })).toHaveCount(1)
})

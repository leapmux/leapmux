import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForControlBanner } from '../helpers/ui'
import { enableNativeCommands } from './scenarios'

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--timeout',
    '40s',
  ]
}

fastAgentTest('reads the archived reply for the exact input and its retry', async ({ native, leapmuxServer }) => {
  await enableNativeCommands(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const question = 'How many items are open after the native archive check?'
  const answer = 'Two items are open after the native archive check.'
  const step = await native.modelScript.queue({ text: answer })
  const send = sendAndWait(agentId, native.modelScript.prompt(question), 'native-archive-reply')

  const result = await runCLI(cli, send) as { input_id: string }
  const expected = {
    id: agentId,
    turn_outcome: 'completed',
    replies: [answer],
  }
  expect(result).toMatchObject(expected)
  expect(typeof result.input_id).toBe('string')
  await native.modelScript.waitForSteps(step + 1)
  expect(await runCLI(cli, send)).toMatchObject({ ...expected, input_id: result.input_id })
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
})

fastAgentTest('reports the exact blocking request and keeps an earlier completed reply readable', async ({ native, leapmuxServer }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const earlierAnswer = 'The readiness check completed.'
  const firstStep = await native.modelScript.queue({ text: earlierAnswer })
  const earlier = sendAndWait(agentId, native.modelScript.prompt('Summarize readiness.'), 'readiness-reply')
  expect(await runCLI(cli, earlier)).toMatchObject({
    turn_outcome: 'completed',
    replies: [earlierAnswer],
  })
  await native.modelScript.waitForSteps(firstStep + 1)

  const answer = 'The approved command printed acp-permission-proof.'
  const step = await native.modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'cli-wait-acp-control', 'printf acp-permission-proof'),
    ],
  }, { text: answer })
  const send = sendAndWait(agentId, native.modelScript.prompt('Run the scripted permission check.'), 'acp-permission-check')
  const blocked = await runCLI(cli, send) as { input_id: string }
  expect(blocked).toMatchObject({
    id: agentId,
    message: 'delivered',
    state: 'waiting_for_user',
  })
  expect(typeof blocked.input_id).toBe('string')
  await waitForControlBanner(native.page)
  expect(await runCLI(cli, earlier)).toMatchObject({
    turn_outcome: 'completed',
    replies: [earlierAnswer],
  })

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [request] = requests
  if (!request)
    throw new Error('The Worker returned no permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    request.id,
  ])
  await native.modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, send)).toMatchObject({
    input_id: blocked.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
})

import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { createNativePermissionFileWrite } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForControlBanner } from '../helpers/ui'
import { bypassToolRequests } from './scenarios'

copilotTest('returns the exact reply for a submitted input and its keyed retry', async ({ native, leapmuxServer, modelScript, page }, testInfo) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the completed verification.'
  const answer = 'The native provider confirmed both checks.'
  await modelScript.queue({ text: answer })

  const command = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(prompt),
    '--request-id',
    'copilot-verification-reply',
    '--wait',
    '--timeout',
    '20s',
  ]
  const first = await runCLI(cli, command) as { input_id: string }
  const snapshot = await readNativeMessageSnapshot(native, agentId)
  await testInfo.attach('copilot-worker-frames', {
    body: JSON.stringify(snapshot.messages.map(nativeMessageBody)),
    contentType: 'application/json',
  })
  expect(first).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  expect(await runCLI(cli, command)).toMatchObject({
    input_id: first.input_id,
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
})

copilotTest('waits through a native tool call before returning the requested reply', async ({ native, leapmuxServer, modelScript, page }, testInfo) => {
  await bypassToolRequests(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const answer = 'The scripted output check completed.'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'copilot-cli-tool-round-trip', 'echo COPILOT_CHECK_42'),
    ],
  }, { text: answer })
  const reply = await runCLI(cli, [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Check the output of the scripted shell tool.'),
    '--wait',
    '--timeout',
    '20s',
  ])
  const snapshot = await readNativeMessageSnapshot(native, agentId)
  await testInfo.attach('copilot-worker-frames', {
    body: JSON.stringify(snapshot.messages.map(nativeMessageBody)),
    contentType: 'application/json',
  })
  expect(reply).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
})

copilotTest('settles at a native permission request and reaches idle after approval', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const operation = await createNativePermissionFileWrite(native, {
    fileName: 'copilot-cli-wait-permission.txt',
    callId: 'copilot-cli-wait-permission',
    outputPrefix: 'COPILOT_CLI_WAIT_APPROVED',
  })
  const answer = 'The approved write completed.'
  const step = await modelScript.queue(
    {
      toolCalls: [operation.toolCall],
    },
    { text: answer },
  )
  const send = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Write the requested verification file.'),
    '--request-id',
    'copilot-cli-wait-permission',
    '--wait',
    '--timeout',
    '20s',
  ]
  const blocked = await runCLI(cli, send) as { input_id: string }
  expect(blocked).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await waitForControlBanner(page)
  await operation.beforeDecision()
  const wait = [
    'agent',
    'wait',
    agentId,
    '--timeout',
    '5s',
    '--until',
  ]
  expect(await runCLI(cli, [...wait, 'settled'])).toMatchObject({
    agents: [
      { id: agentId, state: 'waiting_for_user', reached: true },
    ],
  })

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [request] = requests
  if (!request)
    throw new Error('The Worker returned no Copilot permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    request.id,
  ])
  await modelScript.waitForSteps(step + 2)

  expect(await runCLI(cli, send)).toMatchObject({
    input_id: blocked.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  expect(await runCLI(cli, [...wait, 'idle'])).toMatchObject({
    agents: [
      { id: agentId, state: 'idle', reached: true },
    ],
  })
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
})

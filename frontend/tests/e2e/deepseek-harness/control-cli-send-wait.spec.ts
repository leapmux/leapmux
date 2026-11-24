import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { readNativeInputQueue } from '../helpers/nativeInputQueueIdle'
import { chooseQuestionOption } from '../helpers/nativeQuestion'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall, deepseekHarnessEscalatedBashToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { queuedInputRow } from '../helpers/steer'
import { assistantBubbles, interruptButton, resumePausedQueue, sendMessage, userBubbles, waitForControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'

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
    '20s',
  ]
}

deepseekHarnessTest('returns the exact reply while a later native turn runs', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the first readiness check.'
  const answer = 'The first readiness check completed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'deepseek-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextAnswer = 'The native command produced the expected output.'
  const gate = 'deepseek-finish-tool-turn'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'deepseek-cli-output', 'printf deepseek-cli-output'),
    ],
  }, { text: nextAnswer, gate })
  const second = sendAndWait(agentId, modelScript.prompt('Run the scripted readiness command.'), 'deepseek-tool')
  const retryWhileWorking = async () => {
    try {
      await modelScript.waitForGate(gate)
      expect(await runCLI(cli, first)).toMatchObject({
        input_id: completed.input_id,
        turn_outcome: 'completed',
        replies: [answer],
      })
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  }
  const [next] = await Promise.all([
    runCLI(cli, second),
    retryWhileWorking(),
  ])
  expect(next).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextAnswer })).toHaveCount(1)
})

deepseekHarnessTest('returns the owned permission request and resumes the same input', async ({ askingDeepseekHarnessWorkspace, leapmuxServer, modelScript, page }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingDeepseekHarnessWorkspace.workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const answer = 'The approved command completed.'
  await modelScript.queue({
    toolCalls: [
      deepseekHarnessEscalatedBashToolCall('deepseek-cli-permission', 'printf cli-approved', 'Allow the scripted verification command.'),
    ],
  }, { text: answer })
  const send = sendAndWait(agentId, modelScript.prompt('Run the scripted verification command.'), 'deepseek-permission')
  const waiting = await runCLI(cli, send) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await waitForControlBanner(page)

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [permission] = requests
  if (!permission)
    throw new Error('The Worker returned no DeepSeek Harness permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    permission.id,
  ])
  expect(await runCLI(cli, send)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
})

deepseekHarnessTest('returns the owned question and completes the same input after an answer', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const answer = 'The native question was answered.'
  const question = askUserQuestionToolCall(native.provider, 'deepseek-cli-question', [
    {
      question: 'Which route should I use?',
      header: 'Route',
      options: [
        { label: 'First', description: 'Use the first route.' },
        { label: 'Second', description: 'Use the second route.' },
      ],
    },
  ])
  const step = await modelScript.queue({
    toolCalls: [question],
  }, { text: answer })
  const send = sendAndWait(agentId, modelScript.prompt('Ask the scripted route question.'), 'deepseek-question')
  const waiting = await runCLI(cli, send) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await chooseQuestionOption('Second')(await waitForControlBanner(page))
  await modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, send)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
})

deepseekHarnessTest('returns only the reply after the native steer echo', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const gate = 'hold-native-steer-boundary'
  const before = 'The command is about to run.'
  const after = 'The updated readiness check completed.'
  const steerText = 'Include the updated readiness check.'
  await modelScript.queue({
    text: before,
    gate,
    toolCalls: [
      bashToolCall(native.provider, 'deepseek-cli-steer-boundary', 'printf steer-boundary'),
    ],
  }, { text: after })
  const root = [
    ...sendAndWait(agentId, modelScript.prompt('Run the scripted command.'), 'deepseek-steer-root'),
    '--all',
  ]
  const steer = [
    ...sendAndWait(agentId, modelScript.prompt(steerText), 'deepseek-steer-input'),
    '--all',
  ]
  const deliverSteer = async () => {
    await modelScript.waitForGate(gate)
    return await Promise.all([
      runCLI(cli, steer),
      withCleanup(async () => {
        const row = queuedInputRow(page, steerText)
        await row.getByRole('button', { name: 'Steer', exact: true }).click()
        await expect(row.and(page.locator(`:not([data-input-state="${AgentInputState.AWAITING_ECHO}"])`))).toHaveCount(0)
        expect(await row.count()).toBe(1)
        await expect(userBubbles(page).filter({ hasText: steerText })).toHaveCount(0)
      }, async () => { await modelScript.releaseGateIfHeld(gate) }),
    ])
  }
  const [first, [steered]] = await Promise.all([
    runCLI(cli, root),
    deliverSteer(),
  ])
  expect(first).toMatchObject({
    turn_outcome: 'completed',
    replies: [
      before,
      after,
    ],
  })
  const expectedSteer = {
    input_id: (steered as { input_id: string }).input_id,
    turn_outcome: 'completed',
    replies: [
      after,
    ],
  }
  expect(steered).toMatchObject(expectedSteer)
  expect(await runCLI(cli, steer)).toMatchObject(expectedSteer)
  await expect(userBubbles(page).filter({ hasText: steerText })).toHaveCount(1)
})

deepseekHarnessTest('keeps the original CLI request when a stop removes an unread steer', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const gate = 'hold-native-steer-for-stop'
  const steerText = 'Report the recovered readiness result.'
  const answer = 'The recovered readiness check completed.'
  await modelScript.queue({
    text: 'This held answer will be interrupted.',
    gate,
  }, { text: answer })
  const send = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerText),
    '--request-id',
    'deepseek-requeue-steer',
  ]
  const inputId = await withCleanup(async () => {
    await sendMessage(page, modelScript.prompt('Hold this turn while I queue an update.'))
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, send) as { input_id: string }
    await queuedInputRow(page, steerText).getByRole('button', { name: 'Steer', exact: true }).click()
    await retryUntilPass(async () => {
      const queue = await readNativeInputQueue(leapmuxServer, agentId)
      expect(queue.items.find(item => item.id === queued.input_id)?.state).toBe(AgentInputState.AWAITING_ECHO)
    })
    await interruptButton(page).click()
    await retryUntilPass(async () => {
      const queue = await readNativeInputQueue(leapmuxServer, agentId)
      expect(queue.activeTurn).toBe(false)
      expect(queue.items.find(item => item.id === queued.input_id)?.state).toBe(AgentInputState.QUEUED)
    })
    return queued.input_id
  }, async () => { await modelScript.releaseGateIfHeld(gate) })

  await resumePausedQueue(page)
  expect(await runCLI(cli, [
    ...send,
    '--wait',
    '--timeout',
    '20s',
  ])).toMatchObject({
    input_id: inputId,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await expect(userBubbles(page).filter({ hasText: steerText })).toHaveCount(1)
})

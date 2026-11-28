import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { WorkerAgentService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { chooseQuestionOption } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, steerButton } from '../helpers/steer'
import { inputQueue, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

function keyedSend(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
  ]
}

const waitFlags = ['--wait', '--timeout', '20s']

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return keyedSend(agentId, prompt, requestId).concat(waitFlags)
}

ohMyPiTest('returns the original reply while a later input waits for an answer', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the initial readiness check.'
  const answer = 'The initial readiness check completed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'omp-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextAnswer = 'The native question was answered.'
  const question = askUserQuestionToolCall(native.provider, 'omp-cli-question', [
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
  }, { text: nextAnswer })
  const second = sendAndWait(agentId, modelScript.prompt('Ask the scripted route question.'), 'omp-question')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  const banner = await waitForControlBanner(page)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await chooseQuestionOption('Second')(banner)
  await modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

ohMyPiTest('returns each reply to identical steers by request ID', async ({ native }) => {
  await exerciseSteerReplies(native, false)
})

ohMyPiTest('returns a shared reply to both inputs in a native steer batch', async ({ native }) => {
  await exerciseSteerReplies(native, true)
})

async function exerciseSteerReplies(native: ManagedNativeScenarioContext, batched: boolean): Promise<void> {
  const { leapmuxServer, modelScript, page } = native
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  if (batched) {
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await channel.callWorker(leapmuxServer.workerId, WorkerAgentService.method.sendAgentRawMessage, {
      agentId,
      content: JSON.stringify({ type: 'set_steering_mode', mode: 'all' }),
    })
  }
  const leadGate = 'hold-omp-lead-for-first-steer'
  const firstGate = 'hold-omp-first-steer-for-second-steer'
  const prompt = 'Check the next shipment.'
  const leadReply = 'Two shipments await inspection.'
  const secondReply = 'The second shipment is ready.'
  const firstReply = batched ? secondReply : 'The first shipment needs a new label.'
  const input = modelScript.prompt(prompt)
  const lead = keyedSend(agentId, modelScript.prompt('Count the shipments.'), 'omp-steer-lead')
  const first = keyedSend(agentId, input, 'omp-first-steer')
  const second = keyedSend(agentId, input, 'omp-second-steer')
  const enqueueSteer = async (args: string[]) => {
    const { input_id: inputId } = await runCLI(cli, args) as { input_id: string }
    const row = inputQueue(page).getByTestId(`queued-input-${inputId}`)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    return inputId
  }

  await withCleanup(async () => {
    const continuation = batched
      ? []
      : [
          { text: firstReply, gate: firstGate },
        ]
    const step = await modelScript.queue(
      { text: leadReply, gate: leadGate },
      ...continuation,
      { text: secondReply },
    )
    const { input_id: leadId } = await runCLI(cli, lead) as { input_id: string }
    await modelScript.waitForGate(leadGate)
    const firstId = await enqueueSteer(first)
    if (!batched) {
      await modelScript.releaseGate(leadGate)
      await modelScript.waitForGate(firstGate)
      expect(await runCLI(cli, lead.concat(waitFlags, '--all'))).toMatchObject({
        input_id: leadId,
        state: 'working',
        turn_outcome: 'completed',
        replies: [leadReply],
      })
    }
    const secondId = await enqueueSteer(second)
    expect(secondId).not.toBe(firstId)
    await modelScript.releaseGate(batched ? leadGate : firstGate)
    await modelScript.waitForSteps(step + (batched ? 2 : 3))
    const context = nativeScenarioModelContextText(native, await modelScript.requestAt(step + 1))
    expect(context.split(prompt)).toHaveLength(batched ? 3 : 2)
    expect(context).not.toContain('leapmux-steer:')
    await waitForAgentIdle(page)
    expect(await runCLI(cli, first.concat(waitFlags, '--all'))).toMatchObject({
      input_id: firstId,
      turn_outcome: 'completed',
      replies: [firstReply],
    })
    expect(await runCLI(cli, second.concat(waitFlags, '--all'))).toMatchObject({
      input_id: secondId,
      turn_outcome: 'completed',
      replies: [secondReply],
    })
    expect(await runCLI(cli, lead.concat(waitFlags, '--all'))).toMatchObject({
      input_id: leadId,
      turn_outcome: 'completed',
      replies: [leadReply],
    })
    await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(2)
  }, async () => {
    await modelScript.releaseGateIfHeld(leadGate)
    await modelScript.releaseGateIfHeld(firstGate)
  })
}

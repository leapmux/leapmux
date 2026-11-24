import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

function sendAndWait(agentId: string, text: string, key: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    text,
    '--request-id',
    key,
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
}

museTest('retries the exact reply while a later native question is open', async ({ native, page, leapmuxServer, modelScript }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id } = await currentNativeAgent(native)
  const prompt = 'Summarize the readiness check.'
  const answer = 'The readiness check passed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = sendAndWait(id, modelScript.prompt(prompt), 'muse-ready')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const secondReply = 'The second choice was recorded.'
  await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'muse-cli-question', [NATIVE_CONTROL_QUESTION]),
    ],
  }, nativeTextStep(native, secondReply))
  const second = sendAndWait(id, modelScript.prompt('Ask the scripted native question.'), 'muse-question')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user' })
  await expect(await waitForControlBanner(page)).toContainText(NATIVE_CONTROL_QUESTION.question)
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
    replies: [secondReply],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: secondReply })).toHaveCount(1)
})

museTest('returns only the reply after a native steer is consumed', async ({ native, page, leapmuxServer, modelScript }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id, workingDir } = await currentNativeAgent(native)
  const output = createToolOutputControl(workingDir)
  const prompt = 'Include the revised checklist.'
  const before = 'I will run the checklist command.'
  const after = 'The revised checklist passed.'
  await modelScript.queue({
    text: before,
    toolCalls: [
      bashToolCall(native.provider, 'muse-cli-steer-boundary', output.command),
    ],
  }, nativeTextStep(native, after))
  const root = sendAndWait(id, modelScript.prompt('Run the controlled checklist command.'), 'muse-steer-root')
  const steer = sendAndWait(id, modelScript.prompt(prompt), 'muse-steer-input')
  const deliverSteer = async () => {
    await output.waitForFirstOutput()
    const [result] = await Promise.all([
      runCLI(cli, steer) as Promise<{ input_id: string }>,
      (async () => {
        const row = queuedInputRow(page, prompt)
        await steerButton(row).click()
        await expect(row.and(page.locator(`:not([data-input-state="${AgentInputState.AWAITING_ECHO}"])`))).toHaveCount(0)
        await output.releaseFirstOutput()
        await output.waitForSecondOutput()
        await output.releaseFinalOutput()
      })(),
    ])
    return result
  }
  const [initial, result] = await withCleanup(async () => await Promise.all([
    runCLI(cli, root),
    deliverSteer(),
  ]), async () => {
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
  })
  expect(initial).toMatchObject({
    turn_outcome: 'completed',
    replies: [before, after],
  })
  const expected = {
    input_id: result.input_id,
    turn_outcome: 'completed',
    replies: [after],
  }
  expect(result).toMatchObject(expected)
  expect(await runCLI(cli, steer)).toMatchObject(expected)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

museTest('returns a separate reply for a native steer carried into a new turn', async ({ native, page, leapmuxServer, modelScript }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id } = await currentNativeAgent(native)
  const prompt = 'Check the revised list as well.'
  const initialReply = 'The original list passed.'
  const steerReply = 'The revised list passed.'
  const gate = 'muse-cli-late-steer'
  const firstStep = await modelScript.queue({
    ...nativeTextStep(native, initialReply),
    gate,
  }, nativeTextStep(native, steerReply))
  const root = sendAndWait(id, modelScript.prompt('Check the original list.'), 'muse-late-steer-root')
  const steer = sendAndWait(id, modelScript.prompt(prompt), 'muse-late-steer-input')
  const deliverSteer = async () => {
    await modelScript.waitForGate(gate)
    const [result] = await Promise.all([
      runCLI(cli, steer) as Promise<{ input_id: string }>,
      (async () => {
        const row = queuedInputRow(page, prompt)
        await steerButton(row).click()
        await expect(row).toHaveAttribute('data-input-state', String(AgentInputState.AWAITING_ECHO))
        await modelScript.releaseGate(gate)
      })(),
    ])
    return result
  }
  const [initial, result] = await withCleanup(async () => await Promise.all([
    runCLI(cli, root),
    deliverSteer(),
  ]), async () => { await modelScript.releaseGateIfHeld(gate) })
  expect(initial).toMatchObject({
    turn_outcome: 'completed',
    replies: [initialReply],
  })
  const expected = {
    input_id: result.input_id,
    turn_outcome: 'completed',
    replies: [steerReply],
  }
  expect(result).toMatchObject(expected)
  expect(await runCLI(cli, steer)).toMatchObject(expected)
  expect(nativeScenarioModelContextText(native, await modelScript.requestAt(firstStep + 1))).toContain(prompt)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

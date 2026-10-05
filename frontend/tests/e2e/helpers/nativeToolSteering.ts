import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AgentActivityState, AgentInputState, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, SteerQueuedAgentInputRequestSchema, SteerQueuedAgentInputResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { withCleanup } from './cleanup'
import { currentNativeAgent, nativeAgentById, nativeModelContextText, nativeTextStep } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { expectSteeredReply, queuedInputRow, steerQueuedInput } from './steer'
import { createToolOutputControl } from './toolOutputControl'
import { assistantBubbles, messageContents, sendMessage, userBubbles, waitForAgentIdle } from './ui'

/**
 * Steer only after a real native shell command reports that it runs.
 *
 * `expectDisplayedOutput` states that the provider draws a running command's
 * output. The row then shows the live tail of the first output segment, which
 * is the end of its padding and not its marker (see `firstLiveTail`). The
 * marker still has to reach the model, which the end of the proof checks.
 */
export async function exerciseSteerAfterTool(context: ManagedNativeScenarioContext, options: { expectDisplayedOutput?: boolean } = {}): Promise<void> {
  const agent = await currentNativeAgent(context)
  const output = createToolOutputControl(agent.workingDir)
  const steering = 'Also append the word steered to your final reply.'
  await withCleanup(async () => {
    const start = await context.modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, 'held-native-steer-tool', output.command)] },
      nativeTextStep(context, 'finished steered'),
    )
    await sendMessage(context.page, context.modelScript.prompt('Run the controlled waiting command, then reply with one word: finished.'))
    await context.modelScript.waitForSteps(start + 1)
    await output.waitForFirstOutput()
    await expect.poll(async () => (await nativeAgentById(context, agent.id))?.activityState).toBe(AgentActivityState.WORKING)
    if (options.expectDisplayedOutput ?? true)
      await expect(messageContents(context.page).filter({ hasText: output.firstLiveTail }).first()).toBeVisible()
    await expect(context.page.getByTestId('interrupt-button')).toBeVisible()
    await steerQueuedInput(context.page, { message: steering, match: 'Also append the word steered' })
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    await expectSteeredReply(context.page, 'finished steered', 'last')
    await expect(userBubbles(context.page).filter({ hasText: steering }).first()).toBeVisible()
    // The request after the tool step reads the tool result and the steering message.
    const next = await context.modelScript.requestAt(start + 1)
    expect(nativeModelContextText(next)).toContain(steering)
    expect(nativeModelContextText(next)).toContain(output.firstMarker)
    await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  }, async () => {
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
  })
}

/** Keep an unsupported steer in the queue and deliver it as a separate native turn. */
export async function exerciseQueuedTurnWithoutSteering(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  expect(agent.supportsSteering).toBe(false)
  const marker = randomUUID().replaceAll('-', '')
  const gate = `native-queued-turn-${marker}`
  const nextPrompt = `NEXTQUEUEDPROMPT${marker}`
  const firstAnswer = `FIRSTQUEUEDANSWER${marker}`
  const nextAnswer = `NEXTQUEUEDANSWER${marker}`
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  await withCleanup(async () => {
    const start = await context.modelScript.queue({ ...nativeTextStep(context, firstAnswer), gate }, nativeTextStep(context, nextAnswer))
    await sendMessage(context.page, context.modelScript.prompt('Hold the first ordinary native turn.'))
    await context.modelScript.waitForGate(gate)
    await sendMessage(context.page, context.modelScript.prompt(nextPrompt))
    const readQueue = async () => channel.callWorker(server.workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: agent.id })
    await expect.poll(async () => (await readQueue()).snapshot?.items.filter(item => item.text.includes(nextPrompt)).map(item => ({ state: item.state, canSteer: item.canSteer })))
      .toEqual([{ state: AgentInputState.QUEUED, canSteer: false }])
    const item = (await readQueue()).snapshot?.items.find(candidate => candidate.text.includes(nextPrompt))
    if (!item)
      throw new Error('The unsupported steering probe has no queued Worker input.')
    const row = queuedInputRow(context.page, nextPrompt).filter({ visible: true })
    await expect(row).toBeVisible()
    await expect(row.getByRole('button', { name: 'Steer', exact: true })).toHaveCount(0)
    await expect(channel.callWorker(server.workerId, 'SteerQueuedAgentInput', SteerQueuedAgentInputRequestSchema, SteerQueuedAgentInputResponseSchema, { agentId: agent.id, inputId: item.id }))
      .rejects
      .toMatchObject({ source: 'rpc', code: Code.FailedPrecondition, message: 'agent provider does not support steering' })
    expect((await readQueue()).snapshot?.items.find(candidate => candidate.id === item.id)?.state).toBe(AgentInputState.QUEUED)
    await context.modelScript.releaseGate(gate)
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    // The unsupported steer runs as the next ordinary turn.
    const next = await context.modelScript.requestAt(start + 1)
    expect(nativeModelContextText(next)).toContain(nextPrompt)
    expect(nativeModelContextText(next)).toContain(firstAnswer)
    await expect(assistantBubbles(context.page).filter({ hasText: nextAnswer }).first()).toBeVisible()
    await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
    expect((await readQueue()).snapshot?.items).toEqual([])
  }, async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  })
}

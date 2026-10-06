import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AgentActivityState, AgentInputState, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, SteerQueuedAgentInputRequestSchema, SteerQueuedAgentInputResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { withCleanup } from './cleanup'
import { currentNativeAgent, nativeAgentById, nativeModelContextText, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { expectSteeredReply, queuedInputRow, steerQueuedInput } from './steer'
import { createToolOutputControl } from './toolOutputControl'
import { answerControl, assistantBubbles, messageContents, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner } from './ui'

/**
 * Steer only after a real native shell command reports that it runs.
 *
 * `expectDisplayedOutput` states that the provider draws a running command's
 * output. The row then shows the live tail of the first output segment, which
 * is the end of its padding and not its marker (see `firstLiveTail`). The
 * marker still has to reach the model, which the end of the proof checks.
 *
 * The steer happens while the command holds its first output. The command then
 * writes its second output and ends, so the request after the tool step must
 * hold the start of the first output, the end of the second output, and the
 * steering message. The end of the second output is its padding (see
 * `secondLiveTail`), not its marker: a provider can keep only the start and the
 * end of a large output and drop its middle, where the second marker sits.
 * Cline does this. The command reaches the model only in base64, so the padding
 * in the request can come only from the output.
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
    await retryUntilPass(async () => {
      expect((await nativeAgentById(context, agent.id))?.activityState, 'the Worker reports the agent as working').toBe(AgentActivityState.WORKING)
    })
    if (options.expectDisplayedOutput ?? true)
      await expect(messageContents(context.page).filter({ hasText: output.firstLiveTail }).first()).toBeVisible()
    await expect(context.page.locator('[data-testid="interrupt-button"]:visible')).toBeVisible()
    await steerQueuedInput(context.page, { message: steering, match: 'Also append the word steered' })
    await output.releaseFirstOutput()
    await output.waitForSecondOutput()
    await output.releaseFinalOutput()
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    await expectSteeredReply(context.page, 'finished steered', 'last')
    await expect(userBubbles(context.page).filter({ hasText: steering }).first()).toBeVisible()
    // The request after the tool step reads the tool result, which ends with the output after the steer, and the
    // steering message.
    const next = nativeModelContextText(await context.modelScript.requestAt(start + 1))
    expect(next, 'the next request holds the steering message').toContain(steering)
    expect(next, 'the next request holds the start of the first output').toContain(output.firstMarker)
    expect(next, 'the next request holds the end of the second output, which the command wrote after the steer').toContain(output.secondLiveTail)
    await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  }, async () => {
    await output.releaseFirstOutput()
    await output.releaseFinalOutput()
  })
}

/** How {@link exerciseSteerBeforeTool} answers the permission of its shell command. */
export interface SteerBeforeToolOptions {
  /**
   * Allow the shell command through its visible banner. Set it for a provider whose current mode asks before the
   * command runs. The banner must show the command.
   */
  approveTool?: boolean
  /**
   * The number of turn dividers after the turn. One by default. A provider that ends its turn at the steer and runs
   * the steering message as a new turn draws two.
   */
  resultDividers?: number
}

/**
 * Insert a queued message into the native turn before its first tool runs.
 *
 * A gate holds the tool step until the steer is in the queue, so the steering message reaches the request after the
 * tool step. Return that request, so a caller can check how its provider delivered the message.
 */
export async function exerciseSteerBeforeTool(context: NativeScenarioContext, options: SteerBeforeToolOptions = {}): Promise<MockModelRequestRecord> {
  const gate = `native-steer-before-tool-${uniqueMarker()}`
  const steering = 'Also include the word STEEREDWORD in your reply.'
  const command = 'printf provider-steer-ready'
  const start = await context.modelScript.queue(
    { gate, toolCalls: [bashToolCall(context.provider, 'steer-tool', command)] },
    nativeTextStep(context, 'The turn ended with STEEREDWORD.'),
  )
  await withCleanup(async () => {
    await sendMessage(context.page, context.modelScript.prompt('Run the scripted shell command, then reply.'))
    await context.modelScript.waitForGate(gate)
    await steerQueuedInput(context.page, { message: steering, match: 'Also include the word' })
    await context.modelScript.releaseGate(gate)
  }, async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  })
  if (options.approveTool) {
    const banner = await waitForControlBanner(context.page)
    await expect(banner).toContainText(command)
    await answerControl(context.page, 'allow')
  }
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  // The request after the tool step carries the steering message that the user inserted.
  const next = await context.modelScript.requestAt(start + 1)
  expect(nativeModelContextText(next), 'the steered request holds the inserted message').toContain(steering)
  await expectSteeredReply(context.page, 'STEEREDWORD', 'last')
  await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(options.resultDividers ?? 1)
  return next
}

/** Keep an unsupported steer in the queue and deliver it as a separate native turn. */
export async function exerciseQueuedTurnWithoutSteering(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  expect(agent.supportsSteering, 'the provider cannot steer a running turn').toBe(false)
  const marker = uniqueMarker()
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
    // The held turn is active, so the Worker lets the queued head preempt it.
    await retryUntilPass(async () => {
      expect(
        (await readQueue()).snapshot?.items.filter(item => item.text.includes(nextPrompt)).map(item => ({ state: item.state, canSteer: item.canSteer, canPreempt: item.canPreempt })),
        'the queued prompt waits, offers no steer, and can preempt the held turn',
      ).toEqual([{ state: AgentInputState.QUEUED, canSteer: false, canPreempt: true }])
    })
    const item = (await readQueue()).snapshot?.items.find(candidate => candidate.text.includes(nextPrompt))
    if (!item)
      throw new Error('The unsupported steering probe has no queued Worker input.')
    const row = queuedInputRow(context.page, nextPrompt).filter({ visible: true })
    await expect(row).toBeVisible()
    await expect(row.getByRole('button', { name: 'Steer', exact: true })).toHaveCount(0)
    // The Worker offers preemption to every running root agent that cannot steer (`agentToProto` and
    // `agentSupportsPreemption` in backend/internal/worker/service), and the row then offers Preempt in place of
    // Steer. Read the agent while the turn runs, because the offer needs a running provider.
    const running = await nativeAgentById(context, agent.id)
    expect(running?.supportsPreemption, 'the Worker offers preemption of the held turn').toBe(true)
    await expect(row.getByRole('button', { name: 'Preempt', exact: true })).toHaveCount(1)
    await expect(channel.callWorker(server.workerId, 'SteerQueuedAgentInput', SteerQueuedAgentInputRequestSchema, SteerQueuedAgentInputResponseSchema, { agentId: agent.id, inputId: item.id }))
      .rejects
      .toMatchObject({ source: 'rpc', code: Code.FailedPrecondition, message: 'agent provider does not support steering' })
    expect((await readQueue()).snapshot?.items.find(candidate => candidate.id === item.id)?.state).toBe(AgentInputState.QUEUED)
    expect((await context.modelScript.status()).requests.some(request => request.stepIndex === start + 1), 'the queued prompt waits for the held turn').toBe(false)
    await context.modelScript.releaseGate(gate)
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    // The held turn never read the queued prompt. The unsupported steer runs as the next ordinary turn.
    expect(nativeScenarioModelContextText(context, await context.modelScript.requestAt(start))).not.toContain(nextPrompt)
    const next = nativeScenarioModelContextText(context, await context.modelScript.requestAt(start + 1))
    expect(next).toContain(nextPrompt)
    expect(next).toContain(firstAnswer)
    await expect(assistantBubbles(context.page).filter({ hasText: firstAnswer }).first()).toBeVisible()
    await expect(assistantBubbles(context.page).filter({ hasText: nextAnswer }).first()).toBeVisible()
    // The delivered prompt leaves the page queue as well as the Worker queue. The locator has no `:visible` scope, so
    // a hidden row that the page kept also fails the check.
    await expect(queuedInputRow(context.page, nextPrompt)).toHaveCount(0)
    await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
    expect((await readQueue()).snapshot?.items).toEqual([])
  }, async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  })
}

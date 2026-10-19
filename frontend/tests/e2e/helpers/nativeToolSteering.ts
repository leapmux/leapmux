import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { INTERRUPTION_MARKER } from '../../../src/components/chat/assembledMessage'
import { AgentActivityState, AgentInputState, SteerQueuedAgentInputRequestSchema, SteerQueuedAgentInputResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { withCleanup } from './cleanup'
import { parseGenerationCounters } from './generationProgress'
import { readNativeInputQueue } from './nativeInputQueueIdle'
import { exerciseInterruptedPartialAnswer } from './nativeLifecycle'
import { currentNativeAgent, nativeAgentById, nativeModelContextText, nativeModelConversationTurns, nativeScenarioModelContextText, nativeTextStep } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { expectSteeredReply, queuedInputRow, steerQueuedInput } from './steer'
import { createToolOutputControl } from './toolOutputControl'
import { answerControl, assistantBubbles, expectRowsInOrder, interruptButton, messageContents, resumePausedQueue, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner } from './ui'

export interface SteerDuringTextOptions {
  answer: 'preempt' | 'continue'
  steerOrder?: 'strict' | 'any'
  /** Use the held model stream when native stdout emits no text deltas. */
  progress?: 'counter' | 'model-stream'
}

export interface StopWithPendingSteerOptions {
  /** Acceptance-time rows remain when the provider returns their input to the queue. */
  rowAt?: 'echo' | 'acceptance'
}

/** Stop before the model reads a steer, then send the requeued input exactly once. */
export async function exerciseStopWithPendingSteer(context: ManagedNativeScenarioContext, options: StopWithPendingSteerOptions = {}): Promise<void> {
  if (options.rowAt !== undefined && options.rowAt !== 'echo' && options.rowAt !== 'acceptance')
    throw new Error('The pending steer scenario needs echo or acceptance row timing.')
  const acceptedRows = options.rowAt === 'acceptance' ? 1 : 0
  const { page, modelScript } = context
  const agent = await currentNativeAgent(context)
  const marker = uniqueMarker()
  const steering = `PENDINGSTEER${marker}`
  const answer = `REQUEUEDREPLY${marker}`
  const gate = `native-stop-steer-${marker}`
  await withCleanup(async () => {
    const start = await modelScript.queue(
      { ...nativeTextStep(context, `UNREADANSWER${marker}`), gate },
      nativeTextStep(context, answer),
    )
    await sendMessage(page, modelScript.prompt('Hold the scripted answer for a stop.'))
    await modelScript.waitForGate(gate)
    await steerQueuedInput(page, { message: modelScript.prompt(steering), match: steering })
    await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(acceptedRows)
    await interruptButton(page).click()
    await retryUntilPass(async () => {
      const queue = await readNativeInputQueue(context.leapmuxServer, agent.id)
      expect(queue.activeTurn, 'the native turn ended').toBe(false)
      expect(queue.items.filter(item => item.text.includes(steering)).map(item => item.state), 'the steer returned to the queue once').toEqual([AgentInputState.QUEUED])
    })
    await expect(queuedInputRow(page, steering)).toBeVisible()
    await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(acceptedRows)
    await modelScript.releaseGateIfHeld(gate)
    await resumePausedQueue(page)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const request = await modelScript.requestAt(start + 1)
    const turns = (context.readConversationTurns ?? nativeModelConversationTurns)(request)
    const occurrences = turns.filter(turn => turn.role === 'user').reduce((count, turn) => count + turn.text.split(steering).length - 1, 0)
    expect(occurrences, 'the next request reads the requeued steer exactly once').toBe(1)
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await waitForAgentIdle(page)
      }
      await expect(userBubbles(page).filter({ hasText: steering })).toHaveCount(acceptedRows + 1)
      await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
      await expect(queuedInputRow(page, steering)).toHaveCount(0)
    }
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
}

/** Check the answer and the steer in their native read order, before and after a reload. */
export async function exerciseSteerDuringText(context: ManagedNativeScenarioContext, options: SteerDuringTextOptions): Promise<void> {
  if (options.answer !== 'preempt' && options.answer !== 'continue')
    throw new Error('The steer scenario needs a preempt or continue answer.')
  if (options.steerOrder !== undefined && options.steerOrder !== 'strict' && options.steerOrder !== 'any')
    throw new Error('The steer scenario needs a strict or any steer order.')
  if (options.progress !== undefined && options.progress !== 'counter' && options.progress !== 'model-stream')
    throw new Error('The steer scenario needs counter or model-stream evidence.')
  if (options.answer === 'preempt' && options.progress === 'model-stream')
    throw new Error('The steer scenario needs native partial output for a preempted answer.')
  const marker = uniqueMarker()
  const steering = `STEERINPUT${marker}`
  const reply = `STEEREDREPLY${marker}`
  const steer = async () => {
    await steerQueuedInput(context.page, { message: context.modelScript.prompt(steering), match: steering })
  }
  if (options.answer === 'preempt') {
    await exerciseInterruptedPartialAnswer(context, { cut: steer, followUp: { answer: reply } })
    for (const reload of [false, true]) {
      if (reload)
        await context.page.reload()
      await expect(userBubbles(context.page).filter({ hasText: steering })).toHaveCount(1)
      if (options.steerOrder !== 'any')
        await expectRowsInOrder(messageContents(context.page), [INTERRUPTION_MARKER, steering, reply])
    }
    return
  }
  const head = `ANSWERHEAD${marker}`
  const answer = `${head} ANSWERTAIL${marker}`
  const gate = `native-continue-steer-${marker}`
  await withCleanup(async () => {
    const start = await context.modelScript.queue(
      { ...nativeTextStep(context, answer), text: answer, stream: { chunkChars: head.length, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } },
      nativeTextStep(context, reply),
    )
    await sendMessage(context.page, context.modelScript.prompt('Stream the scripted answer.'))
    await context.modelScript.waitForGate(gate)
    if (options.progress !== 'model-stream') {
      const counts = context.page.locator('[data-testid="thinking-indicator"]:visible [data-animated-count] > :first-child')
      await expect.poll(async () => (await counts.allTextContents()).some(count => (parseGenerationCounters(count).tokens ?? 0) > 0), {
        message: 'the live token counter shows that the Worker received the first chunk',
      }).toBe(true)
    }
    await steer()
    await context.modelScript.releaseGate(gate)
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    expect(nativeScenarioModelContextText(context, await context.modelScript.requestAt(start + 1))).toContain(steering)
    for (const reload of [false, true]) {
      if (reload)
        await context.page.reload()
      await expect(assistantBubbles(context.page).filter({ hasText: answer })).toHaveCount(1)
      await expect(assistantBubbles(context.page).filter({ hasText: reply })).toHaveCount(1)
      await expect(userBubbles(context.page).filter({ hasText: steering })).toHaveCount(1)
      await expect(messageContents(context.page).filter({ hasText: INTERRUPTION_MARKER })).toHaveCount(0)
      await expectRowsInOrder(messageContents(context.page), options.steerOrder === 'any' ? [answer, reply] : [answer, steering, reply])
    }
  }, async () => { await context.modelScript.releaseGateIfHeld(gate) })
}

export const NATIVE_TOOL_STEERING_TEXT = 'Also append the word steered to your final reply.'

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
  const steering = NATIVE_TOOL_STEERING_TEXT
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
    await expect(interruptButton(context.page)).toBeVisible()
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
    const readQueue = () => readNativeInputQueue(server, agent.id)
    // The held turn is active, so the Worker lets the queued head preempt it.
    await retryUntilPass(async () => {
      expect(
        (await readQueue()).items.filter(item => item.text.includes(nextPrompt)).map(item => ({ state: item.state, canSteer: item.canSteer, canPreempt: item.canPreempt })),
        'the queued prompt waits, offers no steer, and can preempt the held turn',
      ).toEqual([{ state: AgentInputState.QUEUED, canSteer: false, canPreempt: true }])
    })
    const item = (await readQueue()).items.find(candidate => candidate.text.includes(nextPrompt))
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
    expect((await readQueue()).items.find(candidate => candidate.id === item.id)?.state).toBe(AgentInputState.QUEUED)
    expect((await context.modelScript.status()).nextStep, 'the queued prompt waits for the held turn').toBeLessThanOrEqual(start + 1)
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
    expect((await readQueue()).items).toEqual([])
  }, async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  })
}

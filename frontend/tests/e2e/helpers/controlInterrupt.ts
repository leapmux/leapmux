import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { QuestionRequest } from './providerToolCalls'
import { existsSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectTurnEndedAfter } from './modelScriptFixture'
import { sendNativeAnswer } from './nativeConversation'
import { readNativeInputQueue } from './nativeInputQueueIdle'
import { currentNativeAgent, nativeAgentById, nativeScenarioModelContextText } from './nativeScenario'
import { askUserQuestionToolCall, bashToolCall } from './providerToolCalls'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { currentIdleReceipt, observeSettledReceipts } from './turnEndSound'
import { expectNoControlBanner, interruptButton, resumePausedQueue, sendMessage, waitForControlBanner } from './ui'

/**
 * The control that the interrupted turn waits on:
 *
 * - `question`: a native question. The banner states the question.
 * - `permission`: a native permission request for a shell command that writes a file. The banner states the file. The
 *   command must never run, so the file stays absent.
 */
export type WaitingControl = 'question' | 'permission'

/** The ID of the native call that raises the waiting control. */
export const WAITING_CONTROL_CALL_ID = 'waiting-control'

/** The question that a waiting `question` control asks. */
export const WAITING_QUESTION: QuestionRequest = {
  question: 'Which color should the interrupted turn use?',
  header: 'Color',
  options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
}

/** The native call that raises a waiting control, and the evidence of that control. */
export interface RaisedControl {
  toolCall: MockModelToolCall
  /** Text that the banner of the control states. */
  bannerText: string
  /** The file that the command of a permission writes. The interrupt must leave it absent. A question has none. */
  guardedFile?: string
}

/**
 * Build the native call that raises `control` for `provider`.
 * A permission writes a file in `workingDir`, whose name holds `marker`. The command uses a path relative to the working directory.
 * The native agent runs the command there, and the banner shows that path.
 */
export function raiseWaitingControl(provider: AgentProvider, control: WaitingControl, workingDir: string, marker: string): RaisedControl {
  if (!/^\w+$/.test(marker))
    throw new Error(`A waiting control needs a marker of word characters, not ${JSON.stringify(marker)}.`)
  if (control === 'question')
    return { toolCall: askUserQuestionToolCall(provider, WAITING_CONTROL_CALL_ID, [WAITING_QUESTION]), bannerText: WAITING_QUESTION.question }
  if (!isAbsolute(workingDir))
    throw new Error(`A waiting permission needs the absolute working directory of the agent, not ${JSON.stringify(workingDir)}.`)
  const fileName = `waiting-control-${marker}.txt`
  return {
    toolCall: bashToolCall(provider, WAITING_CONTROL_CALL_ID, `printf WAITINGCONTROL > ${fileName}`),
    bannerText: fileName,
    guardedFile: join(workingDir, fileName),
  }
}

/** How {@link exerciseControlInterrupt} prepares the session and which control the turn waits on. */
export interface ControlInterruptOptions {
  /** The control that the turn waits on when the reader interrupts it. */
  control: WaitingControl
  /** Prepare the session before its first turn, for example a mode that offers the question tool or asks before a command. */
  prepare?: () => Promise<void>
}

/** The model steps that raise `control`: the call alone. Codewhale 0.10 raised the control on the
 * first call, where an earlier runtime loaded the tool's schema first and needed a second one. */
export function waitingControlSteps(control: RaisedControl): MockModelStep[] {
  return [{ toolCalls: [control.toolCall] }]
}

/**
 * Interrupt a turn that waits on a native control, through the Interrupt control of the banner, and prove these facts:
 *
 * - The Worker withdraws the control: no banner stays on the page, visible or hidden.
 * - The Worker reports the agent as idle, and the browser applies that report.
 * - The stop rings no idle alert, because a move from WAITING_FOR_USER to IDLE is not a settle edge.
 * - The interrupt ends the turn: the agent sends no model request after the call that raised the control.
 * - The Worker pauses the input queue.
 * - The next prompt reaches the same native session, and that session still holds the turn before the interrupt.
 * - A withdrawn permission never ran its command.
 *
 * The provider's own interrupt must release the native control. A native agent that still waits on it cannot answer
 * the next prompt, and the scenario fails there.
 */
export async function exerciseControlInterrupt(context: ManagedNativeScenarioContext, options: ControlInterruptOptions): Promise<void> {
  await options.prepare?.()
  const marker = uniqueMarker()
  // The first turn starts the native session and leaves context that the turn after the interrupt must still read.
  await sendNativeAnswer(context, `Keep CONTROLCONTEXT${marker} for this session.`, `CONTROLANSWER${marker}`)
  const agent = await currentNativeAgent(context)
  const control = raiseWaitingControl(context.provider, options.control, agent.workingDir, marker)
  const steps = waitingControlSteps(control)
  const start = await context.modelScript.queue(...steps)
  const raised = start + steps.length
  await sendMessage(context.page, context.modelScript.prompt('Wait on the scripted control.'))
  await context.modelScript.waitForSteps(raised)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText(control.bannerText)
  // The open control holds the Worker in WAITING_FOR_USER. The move into that state was the settle edge of the turn
  // (see `apply` in agentActivity.store).
  await retryUntilPass(async () => {
    expect((await nativeAgentById(context, agent.id))?.activityState, 'the Worker holds the agent waiting for the user').toBe(AgentActivityState.WAITING_FOR_USER)
  })
  const after = await observeSettledReceipts(context.page)
  await banner.getByTestId('control-interrupt').click()
  await retryUntilPass(async () => {
    expect((await nativeAgentById(context, agent.id))?.activityState, 'the Worker reports the interrupted agent as idle').toBe(AgentActivityState.IDLE)
  })
  // The interrupt withdraws the control, so no banner stays on the page, visible or hidden.
  await expectNoControlBanner(context.page)
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  // The thinking indicator is already absent in WAITING_FOR_USER, and the Worker API reports the state before the
  // browser applies it. The Interrupt button shows only while the browser holds WORKING or WAITING_FOR_USER, so its
  // absence proves that the browser applied the IDLE report. The browser records a receipt in the same task as that
  // state change, so the check below cannot run before it.
  await expect(interruptButton(context.page)).toHaveCount(0)
  // Worker IDLE is an optimistic stop state. The native divider proves that
  // the provider ended its turn before the next prompt enters its session.
  await expect(context.page.locator('[data-testid="result-divider"]:visible').last()).toContainText('interrupted')
  // WAITING_FOR_USER to IDLE is not a settle edge, because the agent was not working. The stop therefore rings no
  // second alert and records no receipt.
  expect(await currentIdleReceipt(context.page, { agentId: agent.id, after }), 'a stopped waiting agent records no idle receipt').toBeUndefined()
  await expectTurnEndedAfter(context.modelScript, raised)
  await retryUntilPass(async () => {
    expect((await readNativeInputQueue(context.leapmuxServer, agent.id)).paused, 'the Worker pauses the input queue after the interrupt').toBe(true)
  })
  await resumePausedQueue(context.page)
  const next = await sendNativeAnswer(context, 'Continue after the withdrawn control.', `AFTERCONTROL${marker}`)
  expect(nativeScenarioModelContextText(context, next)).toContain(`CONTROLANSWER${marker}`)
  expect((await currentNativeAgent(context)).agentSessionId).toBe(agent.agentSessionId)
  if (control.guardedFile !== undefined)
    expect(existsSync(control.guardedFile), 'the withdrawn permission never ran its command').toBe(false)
}

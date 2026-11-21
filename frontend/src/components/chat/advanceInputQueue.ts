import type { QueueHeadAction } from './agentInputQueueDrop'
import { AgentInputQueuePauseReason, AgentInputState } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * What one press of the queue chord can read and do.
 *
 * Every reader reads the CURRENT state when it runs, never a copy taken at the
 * keypress: the steps run across awaits, and a later step must see the
 * snapshot that an earlier one applied.
 */
export interface InputQueueAdvanceSteps {
  /** The action the head offers now, as the head row computes it. */
  headAction: () => QueueHeadAction | undefined
  /** The ID of the head now (the first input that does not await its echo), or undefined. */
  headId: () => string | undefined
  /** The state of the queued input with this ID now, or undefined when the queue no longer holds it. */
  inputState: (inputId: string) => AgentInputState | undefined
  /** The pause reason now. UNSPECIFIED means that the queue runs. */
  pauseReason: () => AgentInputQueuePauseReason
  /** False once the panel shows another agent, or unmounted. */
  stillCurrent: () => boolean
  /** Steer or preempt the head. Resolves false when the call fails. Never rejects. */
  act: (offered: QueueHeadAction) => Promise<boolean>
  /** Resume the paused queue. Resolves true only when the queue resumed. Never rejects. */
  resume: () => Promise<boolean>
}

/**
 * Steer or preempt the head of the input queue, and resume a paused queue, in
 * one press.
 *
 * The order is fixed by what the Worker admits:
 *
 * - **Steer, then resume.** The Worker steers through a pause. A resume that
 *   ran first could let the drain dispatch the head before the steer arrives,
 *   and the Worker would then refuse the steer with an error.
 * - **Resume, then preempt.** The Worker offers no preempt on a paused queue,
 *   so the preempt waits for the snapshot after the resume.
 * - **After the resume, preempt only the head of the keypress, and never
 *   steer.** A resume with no running turn starts a turn for the head, and the
 *   next item then offers a preempt against that new turn. Acting on it would
 *   cancel the turn that this press started. A steer needs no resume, so the
 *   keypress already offered every steer that a resume can allow.
 * - **Resume only the pause that the user saw.** A steer whose delivery fails
 *   still answers with success, and the Worker writes a new pause: either with
 *   a new reason, or, under a delivery-failure pause, with the same reason and
 *   the head left failed or uncertain. A steer on a running queue can pause it
 *   too. Clearing such a pause would hide a failure that nobody has read.
 * - **Resume even when the steer fails** without a new pause. The user asked to
 *   resume, and the head then goes out as the next turn. A failed action does
 *   not run again after the resume.
 * - **Stop when the panel changes agents.** The shell reuses one panel for
 *   every agent tab, so after an await the steps would act on the agent that
 *   the user switched to.
 */
export async function advanceInputQueue(steps: InputQueueAdvanceSteps): Promise<void> {
  const reasonAtKeypress = steps.pauseReason()
  const headAtKeypress = steps.headId()
  const offered = steps.headAction()

  if (offered)
    await steps.act(offered)

  if (reasonAtKeypress === AgentInputQueuePauseReason.UNSPECIFIED)
    return
  if (!steps.stillCurrent() || steps.pauseReason() !== reasonAtKeypress)
    return
  if (offered && headAtKeypress !== undefined && deliveryFailed(steps.inputState(headAtKeypress)))
    return
  const resumed = await steps.resume()
  if (!resumed || offered)
    return

  if (!steps.stillCurrent())
    return
  const afterResume = steps.headAction()
  if (afterResume?.action === 'preempt' && afterResume.head.id === headAtKeypress)
    await steps.act(afterResume)
}

/** Whether an input's delivery failed or may have failed, which waits for an explicit retry. */
function deliveryFailed(state: AgentInputState | undefined): boolean {
  return state === AgentInputState.FAILED || state === AgentInputState.DELIVERY_UNCERTAIN
}

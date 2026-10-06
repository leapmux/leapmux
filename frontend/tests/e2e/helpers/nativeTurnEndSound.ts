import type { MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getUserId } from './api'
import { currentNativeAgent, nativeAgentById, nativeTextStep } from './nativeScenario'
import { readToolCall } from './providerToolCalls'
import { armTurnEndSound, doorbellCount, soundReceiptCursor, waitForIdleSoundReceipt } from './turnEndSound'
import { answerControl, sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

/**
 * The shell command of a sound case. The scenario never reads its output, so the command is as plain as possible:
 * - It holds no shell arithmetic expansion. In Accept Edits, qodercli 1.1.65 runs a plain `echo` at once, but it asks
 *   before a command with an expansion such as `$((40 + 2))`: its permission check keeps the ask at
 *   `mode.accept_edits.ineligible.keep_ask`.
 * - It holds no newline character. Junie 26.9.22 asks for a permission before a command that holds one.
 *
 * A case that runs this command with no `approveTool` must not raise a permission request.
 */
export const NATIVE_SOUND_COMMAND = 'echo SOUND42'

/** The answer that ends each sound turn. */
const SOUND_ANSWER = 'The native sound scenario completed.'

interface NativeTurnEndSoundCommon {
  sound?: 'ding-dong' | 'none'
  prompt?: string
  prepare?: () => Promise<void>
  /** Allow the visible permission request of the tool. The script must run a tool that is not an answer tool. */
  approveTool?: boolean
}

/**
 * One sound case. The script of the case decides whether the turn has tool activity, so the expected sound follows
 * from the script and cannot contradict it.
 */
export type NativeTurnEndSoundCase = NativeTurnEndSoundCommon & (
  | {
    /** The tool of the turn. The provider's answer ends the turn. Absent: a turn that holds only the answer. */
    tool?: MockModelToolCall
    steps?: never
  }
  | {
    /**
     * A custom script, for a provider whose turn does not fit `tool`: one Cursor turn is one Run exchange, so the
     * tool call and the answer form one step.
     */
    steps: readonly MockModelStep[]
    tool?: never
  }
)

/**
 * Return true when the script runs a tool apart from the provider's answer tools.
 * An answer tool delivers the provider's final answer and draws no tool row (Junie's `answer`), so it is no activity.
 */
export function nativeSoundActivity(steps: readonly MockModelStep[], answerToolNames: readonly string[] = []): boolean {
  return steps.some(step => (step.toolCalls ?? []).some(call => !answerToolNames.includes(call.name)))
}

/** The script of one sound case, and the tool activity that the script holds. */
export interface NativeSoundPlan {
  steps: readonly MockModelStep[]
  toolActivity: boolean
}

/**
 * Build the script of one sound case from its tool or its custom script, and derive its tool activity.
 * Refuse a case that gives a tool and a custom script together, an empty custom script, and an approval of a tool
 * that the script does not run. Each check runs before the scenario changes any browser state.
 */
export function planNativeSoundCase(
  options: Pick<NativeTurnEndSoundCommon, 'approveTool'> & { tool?: MockModelToolCall | undefined, steps?: readonly MockModelStep[] | undefined },
  answer: MockModelStep,
  answerToolNames: readonly string[] = [],
): NativeSoundPlan {
  if (options.tool !== undefined && options.steps !== undefined)
    throw new Error('A native sound case gives its tool or its custom script, not both.')
  const steps = options.steps ?? (options.tool === undefined ? [answer] : [{ toolCalls: [options.tool] }, answer])
  if (steps.length === 0)
    throw new Error('The native sound scenario needs a model step.')
  const toolActivity = nativeSoundActivity(steps, answerToolNames)
  if (options.approveTool && !toolActivity)
    throw new Error('A native sound case approves a tool only when its script runs a tool that is not an answer tool.')
  return { steps, toolActivity }
}

/**
 * Write a small file in the working directory of the active agent, and return the provider's call that reads it.
 * A sound case can use this read as its tool in place of a shell command.
 */
export async function nativeSoundReadTool(context: ManagedNativeScenarioContext, callId = 'notification-read'): Promise<MockModelToolCall> {
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-notification-file.txt')
  writeFileSync(path, 'Native notification file contents.\n')
  return readToolCall(context.provider, callId, path)
}

/**
 * Observe the completion sound after the browser handles the actual native idle edge.
 * The doorbell rings once when the sound is `ding-dong` and the turn has tool activity, and never otherwise.
 */
export async function exerciseTurnEndSound(context: ManagedNativeScenarioContext, options: NativeTurnEndSoundCase = {}): Promise<void> {
  const { steps, toolActivity } = planNativeSoundCase(options, nativeTextStep(context, SOUND_ANSWER), context.answerToolNames)
  await options.prepare?.()
  const agent = await currentNativeAgent(context)
  const server = context.leapmuxServer
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  const sound = options.sound ?? 'ding-dong'
  await armTurnEndSound(context.page, userId, sound)
  await currentNativeAgent(context)
  let after = await soundReceiptCursor(context.page)
  const start = await context.modelScript.queue(...steps)
  await sendMessage(context.page, context.modelScript.prompt(options.prompt ?? 'Complete the scripted sound scenario.'))
  if (options.approveTool) {
    await context.modelScript.waitForSteps(start + 1)
    await waitForControlBanner(context.page)
    await expect.poll(async () => (await nativeAgentById(context, agent.id))?.publishedActivityState).toBe(AgentActivityState.WAITING_FOR_USER)
    // Arming the sound reloads the page, so the banner of the open request must show again before the answer.
    await armTurnEndSound(context.page, userId, sound)
    await waitForControlBanner(context.page)
    after = await soundReceiptCursor(context.page)
    await answerControl(context.page, 'allow')
  }
  await context.modelScript.waitForSteps(start + steps.length)
  const receipt = await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  if (!toolActivity)
    expect(receipt.numToolUses, 'a native turn without tools must report explicit zero').toBe(0)
  else if (receipt.numToolUses !== undefined)
    expect(receipt.numToolUses, 'a native turn with tools must retain its activity count').toBeGreaterThan(0)
  await waitForAgentIdle(context.page)
  expect(await doorbellCount(context.page)).toBe(sound === 'ding-dong' && toolActivity ? 1 : 0)
}

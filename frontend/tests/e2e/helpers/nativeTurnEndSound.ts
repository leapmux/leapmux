import type { MockModelStep } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getUserId } from './api'
import { currentNativeAgent, nativeAgentById, nativeTextStep } from './nativeScenario'
import { armTurnEndSound, doorbellCount, soundReceiptCursor, waitForIdleSoundReceipt } from './turnEndSound'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

export interface NativeTurnEndSoundCase {
  toolActivity: boolean
  sound?: 'ding-dong' | 'none'
  steps?: readonly MockModelStep[]
  prompt?: string
  prepare?: () => Promise<void>
  approveTool?: boolean
}

/** Reject a script that contradicts the native activity that the sound case must prove. */
export function assertNativeSoundActivity(steps: readonly MockModelStep[], toolActivity: boolean, answerToolNames: readonly string[] = []): void {
  if (steps.length === 0)
    throw new Error('The native sound scenario needs a model step.')
  const scriptedTools = steps.some(step => (step.toolCalls ?? []).some(call => !answerToolNames.includes(call.name)))
  if (scriptedTools !== toolActivity)
    throw new Error('The native sound script does not match its expected tool activity.')
}

/** Observe completion sound after the browser handles the actual native idle edge. */
export async function exerciseTurnEndSound(context: ManagedNativeScenarioContext, options: NativeTurnEndSoundCase): Promise<void> {
  await options.prepare?.()
  const steps = options.steps ?? [nativeTextStep(context, 'The native sound scenario completed.')]
  assertNativeSoundActivity(steps, options.toolActivity, context.answerToolNames)
  const agent = await currentNativeAgent(context)
  const server = context.leapmuxServer
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  const sound = options.sound ?? 'ding-dong'
  await armTurnEndSound(context.page, userId, sound)
  await currentNativeAgent(context)
  let after = await soundReceiptCursor(context.page)
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue(...steps)
  await sendMessage(context.page, context.modelScript.prompt(options.prompt ?? 'Complete the scripted sound scenario.'))
  if (options.approveTool) {
    await context.modelScript.waitForSteps(start + 1)
    await waitForControlBanner(context.page)
    await expect.poll(async () => (await nativeAgentById(context, agent.id))?.publishedActivityState).toBe(AgentActivityState.WAITING_FOR_USER)
    await armTurnEndSound(context.page, userId, sound)
    await waitForControlBanner(context.page)
    after = await soundReceiptCursor(context.page)
    await context.page.getByTestId('control-allow-btn').filter({ visible: true }).click()
  }
  await context.modelScript.waitForSteps(start + steps.length)
  const receipt = await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  if (!options.toolActivity)
    expect(receipt.numToolUses, 'a native turn without tools must report explicit zero').toBe(0)
  else if (receipt.numToolUses !== undefined)
    expect(receipt.numToolUses, 'a native turn with tools must retain its activity count').toBeGreaterThan(0)
  await waitForAgentIdle(context.page)
  expect(await doorbellCount(context.page)).toBe(sound === 'ding-dong' && options.toolActivity ? 1 : 0)
}

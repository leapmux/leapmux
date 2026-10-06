import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent, nativeModelInstructionText } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { nativeToolResult } from './nativeToolResult'
import { readToolCall } from './providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

/** Complete a native read-only plan from actual file context and inspect its next model request. */
export async function exerciseNativeReadOnlyPlan(
  context: ManagedNativeScenarioContext,
  options: {
    preparePlan: () => Promise<void>
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
  },
): Promise<void> {
  await options.preparePlan()
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native plan proof requires a private working directory.')
  const file = join(agent.workingDir, 'native-read-only-plan-context.txt')
  const marker = 'NATIVE_READ_ONLY_PLAN_CONTEXT'
  writeFileSync(file, `${marker}\n`)
  const callId = 'native-read-only-plan'
  const { resultRequest: request } = await runNativeToolTurn(context, {
    toolCalls: [readToolCall(context.provider, callId, file)],
    prompt: 'Read the supplied context and return the read-only plan.',
    answer: '# Native plan\n\n1. Inspect the file context.\n2. Implement after the user selects execution mode.',
  })
  expect(nativeToolResult(request, callId)).toContain(marker)
  await options.nativeProof(request)
}

/** The trimmed lines of a model instruction text, without the empty ones. */
function instructionLines(text: string): string[] {
  return text.split(/\r?\n/).map(line => line.trim()).filter(line => line !== '')
}

/**
 * Return the instruction lines of `after` that `before` does not hold, joined with newlines.
 * A selected mode adds its own instructions to the request, so these lines are what the mode added.
 */
export function addedInstructionLines(before: string, after: string): string {
  const known = new Set(instructionLines(before))
  return instructionLines(after).filter(line => !known.has(line)).join('\n')
}

/** The instructions that a plan mode adds: a plan, or read-only work. */
const PLAN_INSTRUCTION = /\bplan(?:ning)?\b|read-only|\bread only\b/i

/**
 * Prove that the native Plan mode adds planning instructions to the model request, and that the mode survives a
 * reload. The scenario sends one prompt in the Default mode and the same prompt in the Plan mode, and the second
 * request must hold instructions that the first one does not.
 * `keptSettings` receives the agent before and after the mode change, so the caller proves the settings that its
 * provider keeps through the change.
 */
export async function exerciseNativePlanInstructions(
  context: ManagedNativeScenarioContext,
  options: { keptSettings: (before: AgentInfo, after: AgentInfo) => void },
): Promise<void> {
  const { page } = context
  await waitForNativeSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-default')
  await waitForSettingsIdle(page)
  const before = await currentNativeAgent(context)
  const prompt = 'MODEPROBE return one short response.'
  const normal = await sendNativeAnswer(context, prompt, 'DEFAULT_MODE_REPLY')
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsOptionChosen(page, 'permissionMode-plan')
  await page.reload()
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'permissionMode-plan')
  const planned = await sendNativeAnswer(context, prompt, 'PLAN_MODE_REPLY')
  const additions = addedInstructionLines(nativeModelInstructionText(normal), nativeModelInstructionText(planned))
  expect(additions, 'the Plan mode adds planning instructions to the request').toMatch(PLAN_INSTRUCTION)
  options.keptSettings(before, await currentNativeAgent(context))
}

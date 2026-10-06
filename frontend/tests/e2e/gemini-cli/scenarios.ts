import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent, nativeModelLastUserText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'

/** Build the scenario context of Gemini CLI. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.GEMINI_CLI }
}

/** Use a native file tool to prove automatic approval in autoEdit. */
export async function exerciseGeminiAutoEditWrite(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const marker = randomUUID()
  const path = join(agent.workingDir, `native-autoedit-${marker}.txt`)
  const callId = `gemini-autoedit-${marker}`
  const content = `GEMINI_AUTOEDIT_${marker}\n`
  expect(existsSync(path)).toBe(false)
  const answer = 'The native automatic file write completed.'
  let start: number | undefined
  await expectNoNativeControl(context, { relatedProof: async () => {
    start = await context.modelScript.queue({ toolCalls: [writeToolCall(context.provider, callId, { path, content })] }, { text: answer })
    await sendMessage(context.page, context.modelScript.prompt('Create the file through the native automatic file tool.'))
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    expect(readFileSync(path, 'utf8')).toBe(content)
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  }, testId: 'control-banner' })
  if (start === undefined)
    throw new Error('The native automatic file write queued no step.')
  expect(nativeToolResult(await context.modelScript.requestAt(start + 1), callId)).toContain(`Successfully created and wrote to new file: ${path}.`)
}

/**
 * Prove the installed ACP command catalog does not compact this conversation.
 * Gemini CLI sends the command alone as the last user text of its Google model request.
 */
export async function exerciseNativeCompactCommandLimit(context: ManagedNativeScenarioContext): Promise<void> {
  const { request } = await exerciseCompactAsModelText(context)
  expect(request.protocol).toBe('google-generative-language')
  expect(nativeModelLastUserText(request)).toBe('/compact')
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}

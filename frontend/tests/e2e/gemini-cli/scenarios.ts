import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '../../../src/lib/jsonPick'
import { compactionNoticeRow } from '../helpers/compaction'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Use the native Gemini model protocol with the shared browser scenarios. */
export function nativeContext(context: Omit<ManagedNativeScenarioContext, 'provider'>): ManagedNativeScenarioContext {
  return { ...context, provider: AgentProvider.GEMINI_CLI }
}

/** Use a native file tool to prove automatic approval in autoEdit. */
export async function exerciseGeminiAutoEditWrite(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const marker = randomUUID()
  const path = join(agent.workingDir, `native-autoedit-${marker}.txt`)
  const callId = `gemini-autoedit-${marker}`
  const content = `GEMINI_AUTOEDIT_${marker}\n`
  expect(existsSync(path)).toBe(false)
  const start = (await context.modelScript.status()).stepCount
  const answer = 'The native automatic file write completed.'
  await expectNoNativeControl(context, { relatedControl: async () => {
    await context.modelScript.queue({ toolCalls: [writeToolCall(context.provider, callId, { path, content })] }, { text: answer })
    await sendMessage(context.page, context.modelScript.prompt('Create the file through the native automatic file tool.'))
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    expect(readFileSync(path, 'utf8')).toBe(content)
    await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  }, testId: 'control-banner' })
  const request = (await context.modelScript.status()).requests.find(row => row.stepIndex === start + 1)
  expect(nativeToolResult(request, callId)).toContain(`Successfully created and wrote to new file: ${path}.`)
}

/** Prove the installed ACP command catalog does not compact this conversation. */
export async function exerciseNativeCompactCommandLimit(context: ManagedNativeScenarioContext): Promise<void> {
  const marker = uniqueMarker('GEMINICOMPACT')
  await sendNativeAnswer(context, `Preserve ${marker} in the native conversation.`, `The native context contains ${marker}.`)
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue({ text: 'The native compact command reached the model as text.' })
  await sendMessage(context.page, '/compact')
  const status = await context.modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(context.page)
  const request = status.requests.find(row => row.stepIndex === start)
  if (!request || !isObject(request.body) || !Array.isArray(request.body.contents))
    throw new Error('The native compact command reached no Google model request.')
  const last = request.body.contents.filter(isObject).filter(row => row.role === 'user').at(-1)
  const parts = Array.isArray(last?.parts) ? last.parts.filter(isObject) : []
  expect(parts.map(part => pickString(part, 'text')).join('')).toBe('/compact')
  expect(JSON.stringify(request.body)).toContain(marker)
  await expect(compactionNoticeRow(context.page)).toHaveCount(0)
  await expect(assistantBubbles(context.page).filter({ hasText: 'The native compact command reached the model as text.' })).toBeVisible()
}

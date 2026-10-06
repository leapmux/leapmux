import type { MockModelRequestRecord, MockModelToolCall } from './mockModelScript'
import type { NativeAgentOpenOptions } from './nativeAgentOpen'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { toolInputSchema } from './modelRequestBody'
import { openNativeAgent } from './nativeAgentOpen'
import { sendNativeAnswer } from './nativeConversation'
import { nativeModelToolDescriptors, nativeModelToolNames, nativeToolOutcome } from './nativeScenario'
import { runNativeToolTurn } from './nativeToolExecution'
import { codeExecutionToolCall } from './providerToolCalls'
import { uniqueMarker } from './shellArguments'
import { assistantBubbles, toolCallRow } from './ui'

/**
 * Open a new agent of `providerAgent` through {@link openNativeAgent}, and run one native turn while the tool catalog
 * of the agent is available. Return the model request of that turn: it holds the catalog that the native client
 * offered.
 */
export async function openNativeCatalogTurn(
  context: ManagedNativeScenarioContext,
  providerAgent: ProviderAgent,
  options: NativeAgentOpenOptions = {},
): Promise<MockModelRequestRecord> {
  await openNativeAgent(context, providerAgent, options.workingDir === undefined ? { ...options, directoryPrefix: options.directoryPrefix ?? 'native-code-limit-' } : options)
  return sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
}

interface NativeScriptCase {
  label: string
  source: string
  expected: string
  failed: boolean
}

/** Require output that only real execution can supply, with success and failure coverage. */
export function validateNativeScriptCases(cases: readonly NativeScriptCase[]): void {
  if (cases.length < 2 || !cases.some(item => item.failed) || !cases.some(item => !item.failed))
    throw new Error('The native code scenario requires output and failure cases.')
  for (const script of cases) {
    if (!script.label || !script.source || !script.expected || script.source.includes(script.expected))
      throw new Error('The native code output must require script execution.')
  }
}

/**
 * Check computed native output and script errors in these places:
 *
 * - The model request.
 * - The transcript.
 * - The transcript after a reload.
 */
export async function exerciseNativeCodeExecution(
  context: ManagedNativeScenarioContext,
  options: {
    scripts: (marker: string) => readonly NativeScriptCase[]
    toolCall?: (callId: string, source: string) => MockModelToolCall
    prepare?: () => Promise<void>
    catalogProof?: (request: MockModelRequestRecord) => void | Promise<void>
    nativeProof?: (request: MockModelRequestRecord, callId: string, expected: string, failed: boolean) => Promise<void>
    prepareResultView?: (callId: string, reloaded: boolean) => Promise<void>
    browserProof?: (callId: string, expected: string, failed: boolean) => Promise<void>
  },
): Promise<void> {
  await options.prepare?.()
  const marker = uniqueMarker('NATIVECODE')
  const cases = options.scripts(marker)
  validateNativeScriptCases(cases)
  for (const [index, script] of cases.entries()) {
    const scriptedCallId = `native-code-${index}`
    const call = options.toolCall?.(scriptedCallId, script.source) ?? codeExecutionToolCall(context.provider, scriptedCallId, script.source)
    const callId = call.id
    const { toolRequest, resultRequest: request } = await runNativeToolTurn(context, {
      toolCalls: [call],
      prompt: `Run the native ${script.label} script.`,
      answer: `The native ${script.label} script ended.`,
    })
    await options.catalogProof?.(toolRequest)
    const result = await nativeToolOutcome(context, request, callId)
    expect(result.text).toContain(script.expected)
    if (result.failed !== undefined)
      expect(result.failed).toBe(script.failed)
    await options.nativeProof?.(request, callId, script.expected, script.failed)
    const bubble = toolCallRow(context.page, callId)
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', script.failed ? 'failed' : 'completed')
    await options.prepareResultView?.(callId, false)
    await expect(bubble).toContainText(script.expected)
    await options.browserProof?.(callId, script.expected, script.failed)
    await expect(assistantBubbles(context.page).filter({ hasText: `The native ${script.label} script ended.` }).first()).toBeVisible()
    await context.page.reload()
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', script.failed ? 'failed' : 'completed')
    await options.prepareResultView?.(callId, true)
    await expect(bubble).toContainText(script.expected)
    await options.browserProof?.(callId, script.expected, script.failed)
  }
}

/** Require one real function descriptor and its exact native argument types. */
export function nativeCodeExecutionSchema(request: MockModelRequestRecord, name: string, fields: Readonly<Record<string, string>>): Record<string, unknown> {
  if (!name.trim() || Object.keys(fields).length === 0)
    throw new Error('The native executor schema requires a tool and its argument fields.')
  const matches = nativeModelToolDescriptors(request).filter(tool => tool.name === name)
  if (matches.length !== 1)
    throw new Error(`The native catalog contains ${matches.length} descriptors for ${name}.`)
  const schema = toolInputSchema(matches[0]!)
  if (!isObject(schema) || schema.type !== 'object' || !isObject(schema.properties))
    throw new Error('The native executor has no complete object argument schema.')
  for (const [field, type] of Object.entries(fields)) {
    const property = schema.properties[field]
    if (!isObject(property) || property.type !== type)
      throw new Error(`The native executor argument ${field} has no ${type} schema.`)
  }
  return schema
}

/** Require the real available catalog to exclude the source-audited native executor names. */
export function expectNativeCodeExecutionAbsent(request: MockModelRequestRecord, names: readonly string[]): void {
  if (names.length === 0 || names.some(name => !name.trim()))
    throw new Error('The native code limit requires audited executor names.')
  const catalog = nativeModelToolNames(request)
  for (const name of names)
    expect(catalog).not.toContain(name)
}

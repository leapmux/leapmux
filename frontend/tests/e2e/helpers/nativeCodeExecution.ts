import type { MockModelRequestRecord, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { googleFunctionDeclarations } from './googleModelContent'
import { nativeModelToolNames, nativeTextStep } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { nativeToolResult } from './nativeToolResult'
import { codeExecutionToolCall } from './providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './ui'

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

/** Check computed native output and errors through the model, transcript, and reload paths. */
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
  const marker = `NATIVECODE${randomUUID().replaceAll('-', '')}`
  const cases = options.scripts(marker)
  validateNativeScriptCases(cases)
  for (const [index, script] of cases.entries()) {
    const start = (await context.modelScript.status()).stepCount
    const scriptedCallId = `native-code-${index}`
    const call = options.toolCall?.(scriptedCallId, script.source) ?? codeExecutionToolCall(context.provider, scriptedCallId, script.source)
    const callId = call.id
    await context.modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, `The native ${script.label} script ended.`))
    await sendMessage(context.page, context.modelScript.prompt(`Run the native ${script.label} script.`))
    await waitForNativeToolSteps(context, start + 2)
    const status = await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    const request = status.requests.find(record => record.stepIndex === start + 1)
    if (!request)
      throw new Error('The native script result reached no next model request.')
    const catalogRequest = status.requests.find(record => record.stepIndex === start)
    if (!catalogRequest)
      throw new Error('The native script has no captured tool catalog request.')
    await options.catalogProof?.(catalogRequest)
    const result = context.readToolResult ? await context.readToolResult(request, callId) : { text: nativeToolResult(request, callId) }
    expect(result.text).toContain(script.expected)
    if (result.failed !== undefined)
      expect(result.failed).toBe(script.failed)
    await options.nativeProof?.(request, callId, script.expected, script.failed)
    const bubble = context.page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
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
  const body = isObject(request.body) ? request.body : undefined
  if (!Array.isArray(body?.tools) || body.tools.length === 0)
    throw new Error('The native executor request contains no tool catalog.')
  const tools = request.protocol === 'google-generative-language'
    ? googleFunctionDeclarations(body.tools)
    : body.tools.filter(isObject).map(tool => isObject(tool.function) ? tool.function : tool)
  const matches = tools.filter(tool => tool.name === name)
  if (matches.length !== 1)
    throw new Error(`The native catalog contains ${matches.length} descriptors for ${name}.`)
  const tool = matches[0]!
  const schema = tool.parametersJsonSchema ?? tool.parameters ?? tool.input_schema
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

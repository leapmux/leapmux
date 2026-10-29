import type { CommandResult } from '../../../model/commandResult'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { FASTAGENT_TOOL } from '~/generated/contracts/fastagent-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { retainedOutcome } from '../../registry'
import { fastAgentArchiveSource, fastAgentNativeResultText, sameFastAgentArchiveCall } from '../archiveSource'
import { FAST_AGENT_SHELL } from '../protocol'

function structuredExit(result: Record<string, unknown> | undefined): number | undefined {
  const meta = result
    ? pickObject(result, '_meta')
    : undefined
  const execution = meta
    ? pickObject(meta, FAST_AGENT_SHELL.Metadata)
    : undefined
  const code = execution?.[FAST_AGENT_SHELL.ExitCode]
  return typeof code === 'number' && Number.isSafeInteger(code)
    ? code
    : undefined
}
function commandResult(output: string, code: number | undefined, failed: boolean): CommandResult {
  if (code !== undefined)
    return { output, exitCode: code }
  if (failed)
    return { output, failed: true }
  return { output }
}
export function fastAgentArchiveToolCall(input: RowExtractionInput): ToolCall | null {
  const own = fastAgentArchiveSource(input.resolved)
  if (!own || (own.kind !== 'request' && own.kind !== 'result'))
    return null
  if (input.spanId !== undefined && input.spanId !== own.spanId)
    return null
  const sibling = input.span.result
    ? fastAgentArchiveSource(input.span.result)
    : undefined
  const resultSource = own.kind === 'result'
    ? own
    : sibling?.kind === 'result' && sameFastAgentArchiveCall(own, sibling)
      ? sibling
      : undefined
  const result = resultSource?.result
  const shellTool = own.request.name === FASTAGENT_TOOL.Bash || own.request.name === FASTAGENT_TOOL.Process
  const code = shellTool
    ? structuredExit(result)
    : undefined
  const failed = resultSource?.failed === true || (code !== undefined && code !== 0)
  const lifecycle: ToolCallLifecycleFacts = {
    frameStatus: result
      ? failed ? 'failed' : 'completed'
      : 'in_progress',
    providerOutcome: result
      ? failed ? 'failed' : 'succeeded'
      : null,
    retainedOutcome: retainedOutcome(input.completion ?? input.resolved.completion),
    rowFinal: result !== undefined,
    resultFrameLanded: result !== undefined,
  }
  const envelope: ToolCallEnvelope = { id: own.spanId, name: own.request.name, lifecycle }
  const args = own.request.args
  const output = result
    ? fastAgentNativeResultText(result)
    : ''
  const content = result && Array.isArray(result.content)
    ? result.content.map(parseMcpContentItem)
    : []
  if (own.request.name === FASTAGENT_TOOL.Bash && typeof args.command === 'string') {
    const description = typeof args.description === 'string'
      ? args.description
      : undefined
    const cwd = typeof args.cwd === 'string'
      ? args.cwd
      : undefined
    const artifacts = content.filter(block => block.type !== 'text')
    return createToolCall(envelope, {
      kind: 'execute',
      label: 'Shell',
      request: {
        command: args.command,
        language: 'bash',
        ...(description !== undefined
          ? { description }
          : {}),
        ...(cwd !== undefined
          ? { cwd }
          : {}),
      },
      ...(result
        ? {
            result: {
              commands: [
                commandResult(output, code, failed),
              ],
              unresolvedTerminals: [],
            },
            ...(artifacts.length
              ? { extraContent: artifacts }
              : {}),
          }
        : {}),
    })
  }
  const structured = result && isObject(result.structuredContent)
    ? JSON.stringify(result.structuredContent, null, 2)
    : undefined
  return createToolCall(envelope, {
    kind: 'other',
    label: own.request.name,
    request: { args },
    ...(result
      ? {
          result: {
            content,
            ...(structured !== undefined
              ? { structuredJson: structured }
              : {}),
          },
        }
      : {}),
  })
}

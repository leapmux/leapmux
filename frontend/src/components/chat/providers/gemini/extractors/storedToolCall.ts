import type { ToolCall, ToolCallSpec } from '../../../model/toolCall'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { GEMINI_TOOL } from '~/generated/contracts/gemini-protocol'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { mcpToolCallRequest } from '../../../model/mcpToolCall'
import { failedResult, proseResult, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { retainedOutcome } from '../../registry'
import { geminiAgentRequest, geminiAgentRun } from './agent'
import { geminiCommittedFileChange } from './fileChange'
import { geminiResultContent, geminiResultImages, geminiResultText, geminiShellResult } from './results'

/** Require the exact name and call ID that a native completed tool record stores. */
export function geminiStoredToolRecord(value: unknown): Record<string, unknown> | null {
  if (!isObject(value))
    return null
  const name = pickString(value, 'name')
  const id = pickString(value, 'id')
  return name && id.startsWith(`${name}__`) && id.length > name.length + 2 && ['success', 'error'].includes(pickString(value, 'status')) ? value : null
}

/** Keep the same native task statuses in live and stored tool rows. */
export function geminiTodoItems(value: unknown) {
  return rawTodosToItems(Array.isArray(value) ? value.filter(isObject).map(item => ({ ...item, content: item.description })) : [])
}

/** Read a completed native tool directly into the neutral specification. */
export function geminiStoredToolSpec(record: Record<string, unknown>, interrupted = false): ToolCallSpec {
  const name = pickString(record, 'name')
  const args = pickObject(record, 'args') ?? {}
  const text = geminiResultText(record)
  const failed = record.status === 'error'
  if (name === GEMINI_TOOL.CompleteTask) {
    const payload = args.result
    const submitted = typeof payload === 'string' ? payload : isObject(payload) && typeof payload.response === 'string' ? payload.response : payload === undefined ? '' : prettifyJson(payload)
    return {
      kind: 'report',
      name,
      label: 'Complete Task',
      request: { payload: isObject(payload) ? payload : { result: payload } },
      result: failed ? failedResult(text) : proseResult(submitted, 'markdown'),
      ...(text ? { metadata: [{ label: 'Native result', value: text }] } : {}),
    }
  }
  if (name === GEMINI_TOOL.RunShellCommand) {
    const command = geminiShellResult(record)
    const failure = command.failed === true || (typeof command.exitCode === 'number' && command.exitCode !== 0)
    return { kind: 'execute', name, request: DEFAULT_TOOL_REQUESTS.execute(args), result: { commands: [command], unresolvedTerminals: [] }, ...(!interrupted && failure ? { statusOverride: 'failed' } : {}) }
  }
  if (name === GEMINI_TOOL.ReadFile) {
    const request = DEFAULT_TOOL_REQUESTS.read(args)
    return { kind: 'read', name, request, images: geminiResultImages(record, request.path), result: failed ? failedResult(text) : { lines: null, fallbackContent: text } }
  }
  if (name === GEMINI_TOOL.WriteFile) {
    const request = DEFAULT_TOOL_REQUESTS.write({ ...args, new_string: args.content })
    return { kind: 'write', name, request, result: failed ? failedResult(text) : geminiCommittedFileChange(record) ?? unparsedResult(text) }
  }
  if (name === GEMINI_TOOL.Replace) {
    const request = DEFAULT_TOOL_REQUESTS.edit(args)
    return { kind: 'edit', name, request, result: failed ? failedResult(text) : geminiCommittedFileChange(record) ?? unparsedResult(text) }
  }
  if (name === GEMINI_TOOL.TodoWrite && Array.isArray(args.todos)) {
    const items = geminiTodoItems(args.todos)
    return { kind: 'todo', name, request: { items }, result: failed ? failedResult(text) : { items } }
  }
  if (name === GEMINI_TOOL.InvokeAgent)
    return { kind: 'agent', name, request: geminiAgentRequest(args), result: failed ? failedResult(text) : { agents: [geminiAgentRun(record)] } }
  const display = /^(.+) \((.+) MCP Server\)$/.exec(pickString(record, 'displayName'))
  const mcp = display?.[1] && display[2] ? mcpToolCallRequest(display[2], display[1], args) : { kind: 'mcp' as const, name, request: { server: '', tool: name, args } }
  return { ...mcp, result: { content: geminiResultContent(record), ...(failed ? { error: text } : {}) } }
}

/** Native child tool rows keep their original bytes and use no invented ACP frame. */
export function geminiStoredToolCall(record: Record<string, unknown>, completion?: MessageCompletion): ToolCall {
  const outcome = retainedOutcome(completion)
  return createToolCall({
    id: pickString(record, 'id'),
    name: pickString(record, 'name'),
    lifecycle: { frameStatus: record.status === 'error' ? 'failed' : 'completed', providerOutcome: null, retainedOutcome: outcome, rowFinal: true, resultFrameLanded: true },
  }, geminiStoredToolSpec(record, outcome === 'interrupted'))
}

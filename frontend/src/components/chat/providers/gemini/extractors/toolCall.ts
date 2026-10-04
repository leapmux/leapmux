import type { ToolCallSpec } from '../../../model/toolCall'
import type { ACPToolCallAdapter } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { GEMINI_SUPPLEMENT, GEMINI_TOOL } from '~/generated/contracts/gemini-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'
import { acpSupplementRawOutput } from '../../acp/toolSupplement'
import { geminiAgentRequest } from './agent'
import { geminiStoredToolSpec, geminiTodoItems } from './storedToolCall'

function geminiRecord(tool: Record<string, unknown>, extra: unknown): Record<string, unknown> | null {
  const record = pickObject(acpSupplementRawOutput(isObject(extra) ? extra : undefined), GEMINI_SUPPLEMENT.StoredToolRecord)
  return record && pickString(record, 'id') === pickString(tool, 'toolCallId') && pickString(record, 'name') !== '' ? record : null
}

/** Native tool IDs identify the name even before the JSONL record appears. */
function geminiLiveName(tool: Record<string, unknown>): string {
  const id = pickString(tool, 'toolCallId')
  const separator = id.indexOf('__')
  return separator > 0 ? id.slice(0, separator) : ''
}

export const geminiToolCallAdapter: ACPToolCallAdapter = (facts, base): ToolCallSpec => {
  const record = geminiRecord(facts.tool, facts.extra)
  const name = record ? pickString(record, 'name') : geminiLiveName(facts.tool)
  const args = record ? pickObject(record, 'args') ?? facts.args : facts.args
  if (!name)
    return base()
  if (record && facts.finished)
    return geminiStoredToolSpec(record, facts.retained === 'interrupted' || facts.status === 'cancelled' || facts.status === 'declined')
  const remap = (kind: 'execute' | 'read' | 'write' | 'edit') => acpRemapFacts(facts, { kind, tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: args } })

  if (name === GEMINI_TOOL.RunShellCommand) {
    return { ...acpSpecFor(remap('execute'), 'execute'), name }
  }
  if (name === GEMINI_TOOL.TodoWrite && Array.isArray(args.todos))
    return { kind: 'todo', name, request: { items: geminiTodoItems(args.todos) } }
  if (name === GEMINI_TOOL.InvokeAgent) {
    return { kind: 'agent', name, request: geminiAgentRequest(args) }
  }
  if (name === GEMINI_TOOL.ReadFile) {
    return { ...acpSpecFor(remap('read'), 'read'), name }
  }
  if (name === GEMINI_TOOL.WriteFile)
    return { ...acpSpecFor(remap('write'), 'write'), name }
  if (name === GEMINI_TOOL.Replace)
    return { ...acpSpecFor(remap('edit'), 'edit'), name }
  if (record)
    return { kind: 'mcp', name, request: { server: '', tool: name, args } }
  return { ...base(), name }
}

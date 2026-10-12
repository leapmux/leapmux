import type { ToolCallSpec } from '../../../model/toolCall'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { GEMINI_SUPPLEMENT, GEMINI_TOOL } from '~/generated/contracts/gemini-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'
import { acpSupplementRawOutput } from '../../acp/toolSupplement'
import { declinedToolCallSpec } from '../../declinedToolCall'
import { isGeminiCanceledToolSentence } from '../protocol'
import { GEMINI_TOOL_KINDS, isGeminiTool } from '../toolKinds'
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

/**
 * Gemini CLI's own reading of one call.
 *
 * Gemini CLI hooks the tool NAME. The call identifier states it before `__` from the
 * first frame. The stored session record (`GEMINI_SUPPLEMENT.StoredToolRecord`) states
 * it again, with the arguments and the result, after Gemini CLI writes the call to its
 * session file.
 *
 * The refusal is a post-condition around the WHOLE build, for the reason
 * `providers/README.md` gives: the build has several returns, and a refused call of every
 * tool must read `declined` with the refusal as its body.
 */
export const geminiToolCallAdapter: ACPToolCallAdapter = (facts, base): ToolCallSpec => {
  const spec = geminiToolCall(facts, base)
  return geminiRefused(facts) ? declinedToolCallSpec(spec, facts.text) : spec
}

/**
 * Whether Gemini CLI refused this call on the reader's Deny answer.
 *
 * The frame's OWN status decides with the sentence: only a failed update states a
 * refusal, and a call that completed and printed the same words ran.
 */
function geminiRefused(facts: ACPToolFacts): boolean {
  return facts.lifecycle.frameStatus === 'failed' && isGeminiCanceledToolSentence(facts.text)
}

/**
 * One Gemini CLI call, before the refusal post-condition.
 *
 * A finished call with a stored session record takes the stored reader. A to-do update
 * that carries a list and a subagent launch build from branches of their own. A tool
 * that `GEMINI_TOOL_KINDS` lists takes the shared build at the kind of the table, over
 * the record's arguments when a record exists. Every other call takes the generic card
 * when a record states it, and the shared build at the wire kind when none does.
 */
function geminiToolCall(facts: ACPToolFacts, base: () => ToolCallSpec): ToolCallSpec {
  const record = geminiRecord(facts.tool, facts.extra)
  const name = record ? pickString(record, 'name') : geminiLiveName(facts.tool)
  const args = record ? pickObject(record, 'args') ?? facts.args : facts.args
  if (!name)
    return base()
  if (record && facts.finished)
    return geminiStoredToolSpec(record, facts.retained === 'interrupted' || facts.status === 'cancelled' || facts.status === 'declined')
  if (name === GEMINI_TOOL.TodoWrite && Array.isArray(args.todos))
    return { kind: 'todo', name, request: { items: geminiTodoItems(args.todos) } }
  if (name === GEMINI_TOOL.InvokeAgent)
    return { kind: 'agent', name, request: geminiAgentRequest(args) }
  if (isGeminiTool(name)) {
    const kind = GEMINI_TOOL_KINDS[name]
    return { ...acpSpecFor(acpRemapFacts(facts, { kind, tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: args } }), kind), name }
  }
  if (record)
    return { kind: 'mcp', name, request: { server: '', tool: name, args } }
  return { ...base(), name }
}

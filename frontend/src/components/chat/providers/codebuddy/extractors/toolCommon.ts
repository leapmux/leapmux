import type { CommandExit } from '../../../model/commandResult'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'

import { toolRequestFor } from '../../defaultToolRequests'
import { codebuddyToolKind } from '../toolKinds'

/** One reader per tool kind: the shared default request table. */
const CODEBUDDY_TOOL_READERS: ToolCallSpecReaderTable<CodebuddyToolFacts> = {
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: toolRequestFor('unspecified', facts.args, facts, {}) }),
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: toolRequestFor('other', facts.args, facts, {}) }),
  agent: (facts): ToolCallSpecVariant<'agent'> => ({ kind: 'agent', request: toolRequestFor('agent', facts.args, facts, {}) }),
  agents: (facts): ToolCallSpecVariant<'agents'> => ({ kind: 'agents', request: toolRequestFor('agents', facts.args, facts, {}) }),
  chart: (facts): ToolCallSpecVariant<'chart'> => ({ kind: 'chart', request: toolRequestFor('chart', facts.args, facts, {}) }),
  delete: (facts): ToolCallSpecVariant<'delete'> => ({ kind: 'delete', request: toolRequestFor('delete', facts.args, facts, {}) }),
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', request: toolRequestFor('edit', facts.args, facts, {}) }),
  execute: (facts): ToolCallSpecVariant<'execute'> => ({
    kind: 'execute',
    request: facts.toolName === 'REPL'
      ? { command: pickString(facts.args, 'code') ?? '', language: 'javascript', ...(typeof facts.args.description === 'string' ? { description: facts.args.description } : {}) }
      : toolRequestFor('execute', facts.args, facts, {}),
  }),
  fetch: (facts): ToolCallSpecVariant<'fetch'> => ({ kind: 'fetch', request: toolRequestFor('fetch', facts.args, facts, {}) }),
  glob: (facts): ToolCallSpecVariant<'glob'> => ({ kind: 'glob', request: toolRequestFor('glob', facts.args, facts, {}) }),
  grep: (facts): ToolCallSpecVariant<'grep'> => ({ kind: 'grep', request: toolRequestFor('grep', facts.args, facts, {}) }),
  image: (facts): ToolCallSpecVariant<'image'> => ({ kind: 'image', request: toolRequestFor('image', facts.args, facts, {}) }),
  list: (facts): ToolCallSpecVariant<'list'> => ({ kind: 'list', request: toolRequestFor('list', facts.args, facts, {}) }),
  mcp: (facts): ToolCallSpecVariant<'mcp'> => ({ kind: 'mcp', request: toolRequestFor('mcp', facts.args, facts, {}) }),
  memory: (facts): ToolCallSpecVariant<'memory'> => ({ kind: 'memory', request: toolRequestFor('memory', facts.args, facts, {}) }),
  message: (facts): ToolCallSpecVariant<'message'> => ({ kind: 'message', request: toolRequestFor('message', facts.args, facts, {}) }),
  move: (facts): ToolCallSpecVariant<'move'> => ({ kind: 'move', request: toolRequestFor('move', facts.args, facts, {}) }),
  question: (facts): ToolCallSpecVariant<'question'> => ({ kind: 'question', request: toolRequestFor('question', facts.args, facts, {}) }),
  read: (facts): ToolCallSpecVariant<'read'> => ({ kind: 'read', request: toolRequestFor('read', facts.args, facts, {}) }),
  report: (facts): ToolCallSpecVariant<'report'> => ({ kind: 'report', request: toolRequestFor('report', facts.args, facts, {}) }),
  search: (facts): ToolCallSpecVariant<'search'> => ({ kind: 'search', request: toolRequestFor('search', facts.args, facts, {}) }),
  skill: (facts): ToolCallSpecVariant<'skill'> => ({ kind: 'skill', request: toolRequestFor('skill', facts.args, facts, {}) }),
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => ({ kind: 'switch_mode', request: toolRequestFor('switch_mode', facts.args, facts, {}) }),
  task: (facts): ToolCallSpecVariant<'task'> => ({ kind: 'task', request: toolRequestFor('task', facts.args, facts, {}) }),
  think: (facts): ToolCallSpecVariant<'think'> => ({ kind: 'think', request: toolRequestFor('think', facts.args, facts, {}) }),
  todo: (facts): ToolCallSpecVariant<'todo'> => ({ kind: 'todo', request: toolRequestFor('todo', facts.args, facts, {}) }),
  trigger: (facts): ToolCallSpecVariant<'trigger'> => ({ kind: 'trigger', request: toolRequestFor('trigger', facts.args, facts, {}) }),
  wait: (facts): ToolCallSpecVariant<'wait'> => ({ kind: 'wait', request: toolRequestFor('wait', facts.args, facts, {}) }),
  web_search: (facts): ToolCallSpecVariant<'web_search'> => ({ kind: 'web_search', request: toolRequestFor('web_search', facts.args, facts, {}) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', request: toolRequestFor('write', facts.args, facts, {}) }),
}

/** The facts a live frame or stored function record supplies for one tool call. */
export interface CodebuddyToolFacts {
  callId: string
  toolName: string
  args: Record<string, unknown>
  resultText: string
  isError: boolean
  lifecycle: ToolCallLifecycleFacts
  /** How a shell command ended, when its result states it. */
  commandExit?: CommandExit
  /** The inline output that the native shell renderer supplies. */
  commandOutput?: string
}

/** The native shell tool, whose result is a command result. */
const CODEBUDDY_SHELL_TOOL = 'Bash'

/**
 * How a shell command ended, from the `_meta.rawResponse` of a live result block.
 *
 * CodeBuddy Code 2.160.0 states `exitCode` and `signal` there. The record text states
 * them as well, but a record line cannot be told apart from the same words in the
 * output, so this reads the structured response only.
 */
export function codebuddyCommandExit(block: Record<string, unknown> | undefined): CommandExit | undefined {
  const response = pickObject(pickObject(block, '_meta'), 'rawResponse')
  if (!response)
    return undefined
  const code = response.exitCode
  if (typeof code === 'number' && Number.isSafeInteger(code))
    return { exitCode: code }
  const signal = response.signal
  return typeof signal === 'string' && signal.trim() !== '' ? { signal } : undefined
}

/** Ordinary REPL refusals and spill references can use text instead of the native JSON envelope. */
function nativeReplOutcome(text: string): 'failed' | 'succeeded' | null {
  let value: unknown
  try {
    value = JSON.parse(text)
  }
  catch {
    return null
  }
  if (!isObject(value) || typeof value.stdout !== 'string' || typeof value.stderr !== 'string')
    return null
  if (Object.hasOwn(value, 'error'))
    return typeof value.error === 'string' ? 'failed' : null
  // A successful native result owns its result field, including null, zero, false, and an empty string.
  return Object.hasOwn(value, 'result') ? 'succeeded' : null
}

/**
 * Read one CodeBuddy tool call into the shared model.
 *
 * Live frames carry Anthropic content blocks. Stored Workflow child records
 * carry top-level function calls. Both paths supply the same normalized facts.
 */
export function codebuddyToolCall(facts: CodebuddyToolFacts): ToolCall {
  const kind: ToolKind = codebuddyToolKind(facts.toolName)
  const outcome = facts.toolName === 'REPL' ? nativeReplOutcome(facts.resultText) : null
  const isError = facts.isError || outcome === 'failed'
  const envelope: ToolCallEnvelope = {
    id: facts.callId,
    name: facts.toolName,
    lifecycle: { ...facts.lifecycle, providerOutcome: facts.lifecycle.providerOutcome ?? (isError ? 'failed' : null) },
  }
  const spec = readToolCallSpec(CODEBUDDY_TOOL_READERS, kind, facts)
  // A shell result uses its native inline output when available.
  // The original record remains the fallback for a spill or missing output.
  if (spec.kind === 'execute' && facts.toolName === CODEBUDDY_SHELL_TOOL && facts.lifecycle.resultFrameLanded) {
    return createToolCall(envelope, {
      ...spec,
      result: { commands: [{ output: facts.commandOutput ?? facts.resultText, ...facts.commandExit }], unresolvedTerminals: [] },
    })
  }
  return createToolCall(envelope, {
    ...spec,
    ...(facts.resultText ? { result: isError ? failedResult(facts.resultText) : unparsedResult(facts.resultText) } : {}),
  })
}

/** The first content block of the given type in an Anthropic-shaped message. */
export function anthropicBlock(
  payload: Record<string, unknown>,
  blockType: string,
): Record<string, unknown> | undefined {
  const message = isObject(payload.message) ? payload.message : undefined
  const content = message && Array.isArray(message.content) ? message.content : []
  const block = content.find(item => isObject(item) && pickString(item, 'type') === blockType)
  return block && isObject(block) ? block : undefined
}

/** Every content block of the given type in an Anthropic-shaped message. */
export function anthropicBlocks(
  payload: Record<string, unknown>,
  blockType: string,
): Record<string, unknown>[] {
  const message = isObject(payload.message) ? payload.message : undefined
  const content = message && Array.isArray(message.content) ? message.content : []
  return content.filter(item => isObject(item) && pickString(item, 'type') === blockType) as Record<string, unknown>[]
}

/** The plain text of a `text` or `thinking` block. */
export function anthropicBlockText(block: Record<string, unknown> | undefined, field: string): string {
  if (!block)
    return ''
  const value = block[field]
  return typeof value === 'string' ? value : ''
}

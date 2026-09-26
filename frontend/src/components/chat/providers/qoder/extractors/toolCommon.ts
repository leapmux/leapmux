import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import { isObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'

import { toolRequestFor } from '../../defaultToolRequests'
import { qoderToolKind } from '../toolKinds'

/** One reader per tool kind: the shared default request table. */
const ANTHROPIC_TOOL_READERS: ToolCallSpecReaderTable<AnthropicToolFacts> = {
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: toolRequestFor('unspecified', facts.args, facts, {}) }),
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: toolRequestFor('other', facts.args, facts, {}) }),
  agent: (facts): ToolCallSpecVariant<'agent'> => ({ kind: 'agent', request: toolRequestFor('agent', facts.args, facts, {}) }),
  agents: (facts): ToolCallSpecVariant<'agents'> => ({ kind: 'agents', request: toolRequestFor('agents', facts.args, facts, {}) }),
  chart: (facts): ToolCallSpecVariant<'chart'> => ({ kind: 'chart', request: toolRequestFor('chart', facts.args, facts, {}) }),
  delete: (facts): ToolCallSpecVariant<'delete'> => ({ kind: 'delete', request: toolRequestFor('delete', facts.args, facts, {}) }),
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', request: toolRequestFor('edit', facts.args, facts, {}) }),
  execute: (facts): ToolCallSpecVariant<'execute'> => ({ kind: 'execute', request: toolRequestFor('execute', facts.args, facts, {}) }),
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

/** The facts one Anthropic-shaped tool call carries. */
export interface AnthropicToolFacts {
  callId: string
  toolName: string
  args: Record<string, unknown>
  resultText: string
  isError: boolean
  lifecycle: ToolCallLifecycleFacts
}

/**
 * Read one Anthropic-shaped tool call into the shared model.
 *
 * Both CodeBuddy Code and Qoder CLI speak Anthropic content blocks: an
 * assistant `tool_use` carries the id, name and input, and a user
 * `tool_result` carries the output. The shared default request table reads the
 * arguments; neither provider spells its tool arguments differently from it.
 */
export function anthropicToolCall(facts: AnthropicToolFacts): ToolCall {
  const kind: ToolKind = qoderToolKind(facts.toolName)
  const envelope: ToolCallEnvelope = { id: facts.callId, name: facts.toolName, lifecycle: facts.lifecycle }
  const spec = readToolCallSpec(ANTHROPIC_TOOL_READERS, kind, facts)
  return createToolCall(envelope, {
    ...spec,
    ...(facts.resultText ? { result: facts.isError ? failedResult(facts.resultText) : unparsedResult(facts.resultText) } : {}),
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

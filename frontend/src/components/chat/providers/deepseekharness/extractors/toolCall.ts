import type { ToolCall, ToolCallEnvelope, ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_TOOL } from '~/generated/contracts/deepseek-harness-protocol'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem, parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, proseResult, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { deepseekHarnessQuestionRecords, deepseekHarnessQuestions } from '../askUserQuestion'
import { deepseekHarnessCallId, deepseekHarnessContentText, deepseekHarnessEventData } from '../protocol'
import { deepseekHarnessToolKind } from '../toolKinds'
import { deepseekHarnessCommandResult } from './execute'
import { deepseekHarnessAppliedChanges, deepseekHarnessCreatedChange } from './fileEdit'

interface ToolFacts {
  name: string
  args: Record<string, unknown>
  text: string
  blocks: Record<string, unknown>[]
  meta: Record<string, unknown> | undefined
  landed: boolean
  failed: boolean
  envelope: ToolCallEnvelope
}

function generic<K extends ToolKind>(kind: K, facts: ToolFacts): ToolCall {
  const spec: ToolCallSpecVariant<K> = {
    kind,
    request: DEFAULT_TOOL_REQUESTS[kind](facts.args),
    ...(facts.landed ? { result: facts.failed ? failedResult(facts.text) : unparsedResult(facts.text) } : {}),
  }
  return createToolCall(facts.envelope, spec)
}

function argumentsObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'string')
    return {}
  try {
    const decoded: unknown = JSON.parse(value)
    return isObject(decoded) ? decoded : {}
  }
  catch {
    return {}
  }
}

export function deepseekHarnessToolCall(input: RowExtractionInput): ToolCall | null {
  const own = input.resolved.parentObject
  const id = deepseekHarnessCallId(own)
  if (!own || !id)
    return null
  const same = (payload: unknown) => deepseekHarnessCallId(payload) === id ? payload : undefined
  const request = deepseekHarnessEventData(own, DEEPSEEK_HARNESS_EVENT.ToolCall)
    ?? deepseekHarnessEventData(same(input.span.request?.parentObject), DEEPSEEK_HARNESS_EVENT.ToolCall)
  const result = deepseekHarnessEventData(own, DEEPSEEK_HARNESS_EVENT.ToolResult)
    ?? deepseekHarnessEventData(same(input.span.result?.parentObject), DEEPSEEK_HARNESS_EVENT.ToolResult)
  const message = pickObject(result, 'message')
  const name = pickString(request, 'name') || input.spanType || ''
  const blocks = Array.isArray(message?.content) ? message.content.filter(isObject) : []
  const completion = input.completion ?? input.resolved.completion
  const facts: ToolFacts = {
    name,
    args: argumentsObject(request?.arguments),
    text: deepseekHarnessContentText(blocks),
    blocks,
    meta: pickObject(result, 'meta') ?? undefined,
    landed: result !== undefined,
    failed: message?.isError === true,
    envelope: {
      id,
      name,
      lifecycle: {
        frameStatus: result === undefined ? 'in_progress' : 'unstated',
        providerOutcome: message?.isError === true ? 'failed' : null,
        retainedOutcome: retainedOutcome(completion),
        rowFinal: result !== undefined || retainedRowIsFinal(completion),
        resultFrameLanded: result !== undefined,
      },
    },
  }
  const declared = deepseekHarnessToolKind(name)
  const kind = declared === 'unspecified' ? 'mcp' : declared
  if (facts.failed && kind !== 'mcp' && kind !== 'execute')
    return generic(kind, facts)
  switch (kind) {
    case 'execute': {
      const script = name === DEEPSEEK_HARNESS_TOOL.Workflow || name === DEEPSEEK_HARNESS_TOOL.RunCode
      const request = script
        ? { command: pickString(facts.args, 'script') || pickString(facts.args, 'code'), language: 'javascript' as const }
        : DEFAULT_TOOL_REQUESTS.execute(facts.args)
      const command = script ? { output: facts.text } : deepseekHarnessCommandResult(facts.text)
      return createToolCall(facts.envelope, { kind: 'execute', request, ...(facts.landed ? { result: facts.failed ? failedResult(facts.text) : { commands: [command], unresolvedTerminals: [] } } : {}) })
    }
    case 'read': {
      const images = facts.blocks.flatMap((block) => {
        const image = parseImageBlock(block)
        return image ? [image] : []
      })
      return createToolCall(facts.envelope, { kind: 'read', request: DEFAULT_TOOL_REQUESTS.read(facts.args), ...(facts.landed ? { result: { lines: null, fallbackContent: facts.text }, images } : {}) })
    }
    case 'write':
    case 'edit': {
      const request = DEFAULT_TOOL_REQUESTS[kind](facts.args)
      const changes = deepseekHarnessAppliedChanges(facts.meta)
      const created = kind === 'write' ? deepseekHarnessCreatedChange(facts.meta, facts.args) : undefined
      const applied = created ? [created] : changes
      return createToolCall(facts.envelope, { kind, request, ...(facts.landed ? { result: applied === undefined ? unparsedResult(facts.text) : { changes: applied } } : {}) })
    }
    case 'mcp': {
      const tool = parseMcpToolName(name)
      return createToolCall(facts.envelope, { kind: 'mcp', request: { server: tool?.server ?? '', tool: tool?.tool ?? name, args: facts.args }, ...(facts.landed ? { result: { content: facts.blocks.map(parseMcpContentItem) } } : {}) })
    }
    case 'todo': {
      const items = input.todoSnapshot ? [input.todoSnapshot] : []
      return createToolCall(facts.envelope, { kind: 'todo', request: { items }, ...(facts.landed ? { result: { items, note: facts.text } } : {}) })
    }
    case 'agent': {
      const request = DEFAULT_TOOL_REQUESTS.agent(facts.args)
      const childId = /^started subagent (\S+)$/.exec(facts.text)?.[1]
      return createToolCall(facts.envelope, { kind: 'agent', request, ...(facts.landed ? { result: { agents: [{ description: request.description, agentId: childId ?? '', outcome: childId ? 'running' : 'completed', metadata: [], body: facts.text }] } } : {}) })
    }
    case 'question': {
      const questions = deepseekHarnessQuestions({ questions: deepseekHarnessQuestionRecords(facts.args) })
      return createToolCall(facts.envelope, { kind: 'question', request: { questions }, ...(facts.landed ? { result: { answers: [{ header: questions[0]?.question ?? 'Answer', answer: facts.text || null }] } } : {}) })
    }
    case 'switch_mode':
      return createToolCall(facts.envelope, { kind: 'switch_mode', request: { mode: 'act' }, icon: 'plan-exit', ...(facts.landed ? { result: proseResult(facts.text) } : {}) })
    default:
      return generic(kind, facts)
  }
}

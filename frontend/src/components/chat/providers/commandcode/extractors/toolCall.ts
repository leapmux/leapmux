import type { ToolCall, ToolCallEnvelope, ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { COMMAND_CODE_EVENT } from '~/generated/contracts/commandcode-protocol'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem, parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { commandCodeError, commandCodeEvent, commandCodeText } from '../protocol'
import { COMMAND_CODE_TOOL_RESULTS } from '../spanRole'
import { commandCodeToolKind } from '../toolKinds'

interface ToolFacts {
  name: string
  args: Record<string, unknown>
  text: string
  blocks: Record<string, unknown>[]
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

export function commandCodeToolCall(input: RowExtractionInput): ToolCall | null {
  const own = commandCodeEvent(input.resolved.parentObject)
  const id = pickString(own, 'toolCallId')
  if (!own || !id)
    return null
  const same = (event: Record<string, unknown> | undefined) => event && pickString(event, 'toolCallId') === id ? event : undefined
  const siblingRequest = same(commandCodeEvent(input.span.request?.parentObject))
  const siblingResult = same(commandCodeEvent(input.span.result?.parentObject))
  const request = own.type === COMMAND_CODE_EVENT.ToolQueued ? own : siblingRequest?.type === COMMAND_CODE_EVENT.ToolQueued ? siblingRequest : undefined
  const result = COMMAND_CODE_TOOL_RESULTS.has(pickString(own, 'type')) ? own : COMMAND_CODE_TOOL_RESULTS.has(pickString(siblingResult, 'type')) ? siblingResult : undefined
  const name = pickString(request, 'toolName') || pickString(own, 'toolName')
  const blocks = result && Array.isArray(result.result) ? result.result.filter(isObject) : []
  const text = result ? commandCodeText(result.result) || commandCodeError(result.error) || pickString(result, 'hookOutput') || pickString(result, 'message') : ''
  const declined = result?.type === COMMAND_CODE_EVENT.ToolDenied || result?.type === COMMAND_CODE_EVENT.ToolHookBlocked
  const failed = result?.type === COMMAND_CODE_EVENT.ToolErrored
  const facts: ToolFacts = {
    name,
    args: pickObject(request, 'input') ?? {},
    blocks,
    text,
    landed: result !== undefined,
    failed: failed || declined,
    envelope: {
      id,
      name,
      lifecycle: {
        frameStatus: 'unstated',
        providerOutcome: declined ? 'declined' : failed ? 'failed' : null,
        retainedOutcome: retainedOutcome(input.completion ?? input.resolved.completion),
        rowFinal: result !== undefined || retainedRowIsFinal(input.completion ?? input.resolved.completion),
        resultFrameLanded: result !== undefined,
      },
    },
  }
  const kind = commandCodeToolKind(name)
  if (facts.failed)
    return generic(kind, facts)
  switch (kind) {
    case 'execute': {
      const request = DEFAULT_TOOL_REQUESTS.execute(facts.args)
      const args = Array.isArray(facts.args.args) ? facts.args.args.filter((item): item is string => typeof item === 'string') : []
      request.command += args.length ? ` ${args.map(item => JSON.stringify(item)).join(' ')}` : ''
      return createToolCall(facts.envelope, {
        kind: 'execute',
        request,
        label: 'Shell',
        ...(facts.landed ? { result: { commands: [{ output: facts.text }], unresolvedTerminals: [] } } : {}),
      })
    }
    case 'read': {
      const images = facts.blocks.flatMap((block) => {
        const image = parseImageBlock(block)
        return image ? [image] : []
      })
      return createToolCall(facts.envelope, {
        kind: 'read',
        request: DEFAULT_TOOL_REQUESTS.read(facts.args),
        ...(facts.landed ? { result: { lines: null, fallbackContent: facts.text }, images } : {}),
      })
    }
    case 'write':
      return generic(kind, { ...facts, args: { ...facts.args, new_string: pickString(facts.args, 'content') } })
    case 'edit':
      return generic(kind, facts)
    case 'mcp': {
      const tool = parseMcpToolName(name)
      return createToolCall(facts.envelope, {
        kind: 'mcp',
        request: { server: tool?.server ?? '', tool: tool?.tool ?? name, args: facts.args },
        ...(facts.landed ? { result: { content: facts.blocks.map(parseMcpContentItem) } } : {}),
      })
    }
    case 'todo': {
      const items = input.todoSnapshot ? [input.todoSnapshot] : []
      return createToolCall(facts.envelope, { kind: 'todo', request: { items }, ...(facts.landed ? { result: { items, note: facts.text } } : {}) })
    }
    case 'agent': {
      const request = DEFAULT_TOOL_REQUESTS.agent(facts.args)
      const agentType = pickString(facts.args, 'subagent_type')
      const background = facts.args.run_in_background === true
      const nativeID = /^agent_id: ([\w-]+)$/m.exec(facts.text)?.[1]
      return createToolCall(facts.envelope, {
        kind: 'agent',
        request: { ...request, ...(agentType ? { agentType } : {}) },
        ...(facts.landed
          ? { result: { agents: [{
              description: request.description,
              agentId: nativeID ?? facts.envelope.id,
              outcome: background ? 'running' : 'completed',
              metadata: [],
              body: facts.text,
            }] } }
          : {}),
      })
    }
    default:
      return generic(kind, facts)
  }
}

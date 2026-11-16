import type { CommandResult } from '../../../model/commandResult'
import type { ToolCall, ToolCallEnvelope, ToolCallSpecVariant, ToolFailureResult } from '../../../model/toolCall'
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

/**
 * The result of one call: none before a finished frame lands, the native text of a
 * call that failed or that a rule refused, and the typed result otherwise.
 *
 * A failure replaces only the result. The call keeps the kind, the request and the
 * label that its arguments state, so its header reads the same before and after the
 * failure lands.
 */
function landedResult<R>(facts: ToolFacts, typed: () => R): { result?: R | ToolFailureResult } {
  if (!facts.landed)
    return {}
  return {
    result: facts.failed
      ? failedResult(facts.text)
      : typed(),
  }
}

function generic<K extends ToolKind>(kind: K, facts: ToolFacts): ToolCall {
  const spec: ToolCallSpecVariant<K> = {
    kind,
    request: DEFAULT_TOOL_REQUESTS[kind](facts.args),
    ...landedResult(facts, () => unparsedResult(facts.text)),
  }
  return createToolCall(facts.envelope, spec)
}

/**
 * The lines that Command Code 1.74.1 writes before the output of a command that exits
 * nonzero (`formatResult2` and its sibling in the CLI bundle): `Exit code: N`, a
 * reading of the code in parentheses on the same line or on the next one, and the
 * `Signal:` line. A command that succeeds gets no such line, so a zero code never
 * opens the text.
 */
const COMMAND_CODE_EXIT_LINES = /^Exit code: (-?\d+)(?: \([^\n]*\))?(?:\n\([^\n]*\))?(?:\nSignal: [^\n]*)?(?:\n|$)/

/**
 * One shell command and its code.
 *
 * Command Code completes the call whatever the code is, and states the code only in
 * these lines. A successful command can print the same words as its first line, and
 * nothing tells the two apart; the zero code is the one case that this reading excludes.
 */
function commandCodeShellResult(text: string): CommandResult {
  const lines = COMMAND_CODE_EXIT_LINES.exec(text)
  const exitCode = lines
    ? Number(lines[1])
    : 0
  if (!lines || !Number.isSafeInteger(exitCode) || exitCode === 0)
    return { output: text }
  return {
    output: text.slice(lines[0].length),
    exitCode,
  }
}

/**
 * One native call from the row's own frame and the earlier queued request.
 *
 * A queued request builds its call from its own frame only, so it keeps its kind and
 * its arguments whatever the finished frame states. Only a finished frame carries the
 * output and the failure.
 */
export function commandCodeToolCall(input: RowExtractionInput): ToolCall | null {
  const own = commandCodeEvent(input.resolved.parentObject)
  const id = pickString(own, 'toolCallId')
  if (!own || !id)
    return null
  const siblingRequest = commandCodeEvent(input.span.request?.parentObject)
  const request = own.type === COMMAND_CODE_EVENT.ToolQueued
    ? own
    : siblingRequest?.type === COMMAND_CODE_EVENT.ToolQueued && pickString(siblingRequest, 'toolCallId') === id
      ? siblingRequest
      : undefined
  const result = COMMAND_CODE_TOOL_RESULTS.has(pickString(own, 'type'))
    ? own
    : undefined
  const name = pickString(request, 'toolName') || pickString(own, 'toolName')
  const blocks = result && Array.isArray(result.result)
    ? result.result.filter(isObject)
    : []
  const text = result
    ? commandCodeText(result.result) || commandCodeError(result.error) || pickString(result, 'hookOutput') || pickString(result, 'message')
    : ''
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
        providerOutcome: declined
          ? 'declined'
          : failed ? 'failed' : null,
        retainedOutcome: retainedOutcome(input.completion ?? input.resolved.completion),
        rowFinal: result !== undefined || retainedRowIsFinal(input.completion ?? input.resolved.completion),
        resultFrameLanded: result !== undefined,
      },
    },
  }
  const kind = commandCodeToolKind(name)
  switch (kind) {
    case 'execute': {
      const request = DEFAULT_TOOL_REQUESTS.execute(facts.args)
      const args = Array.isArray(facts.args.args)
        ? facts.args.args.filter((item): item is string => typeof item === 'string')
        : []
      request.command += args.length
        ? ` ${args.map(item => JSON.stringify(item)).join(' ')}`
        : ''
      return createToolCall(facts.envelope, {
        kind: 'execute',
        request,
        label: 'Shell',
        ...landedResult(facts, () => ({
          commands: [
            commandCodeShellResult(facts.text),
          ],
          unresolvedTerminals: [],
        })),
      })
    }
    case 'read': {
      const images = facts.blocks.flatMap((block) => {
        const image = parseImageBlock(block)
        return image
          ? [image]
          : []
      })
      return createToolCall(facts.envelope, {
        kind: 'read',
        request: DEFAULT_TOOL_REQUESTS.read(facts.args),
        ...landedResult(facts, () => ({ lines: null, fallbackContent: facts.text })),
        // A failed or refused call holds no picture.
        ...(facts.landed && !facts.failed
          ? { images }
          : {}),
      })
    }
    case 'write':
      return generic(kind, {
        ...facts,
        args: {
          ...facts.args,
          new_string: pickString(facts.args, 'content'),
        },
      })
    case 'edit':
      return generic(kind, facts)
    case 'mcp': {
      const tool = parseMcpToolName(name)
      return createToolCall(facts.envelope, {
        kind: 'mcp',
        request: { server: tool?.server ?? '', tool: tool?.tool ?? name, args: facts.args },
        ...landedResult(facts, () => ({
          content: facts.blocks.map(parseMcpContentItem),
        })),
      })
    }
    case 'todo': {
      const items = input.todoSnapshot
        ? [input.todoSnapshot]
        : []
      return createToolCall(facts.envelope, {
        kind: 'todo',
        request: { items },
        ...landedResult(facts, () => ({ items, note: facts.text })),
      })
    }
    case 'agent': {
      const request = DEFAULT_TOOL_REQUESTS.agent(facts.args)
      const agentType = pickString(facts.args, 'subagent_type')
      const background = facts.args.run_in_background === true
      const nativeID = /^agent_id: ([\w-]+)$/m.exec(facts.text)?.[1]
      return createToolCall(facts.envelope, {
        kind: 'agent',
        request: {
          ...request,
          ...(agentType
            ? { agentType }
            : {}),
        },
        ...landedResult(facts, () => ({
          agents: [
            {
              description: request.description,
              agentId: nativeID ?? facts.envelope.id,
              outcome: background ? 'running' as const : 'completed' as const,
              metadata: [],
              body: facts.text,
            },
          ],
        })),
      })
    }
    default:
      return generic(kind, facts)
  }
}

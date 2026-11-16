import type { CommandResult } from '../../../model/commandResult'
import type { ToolCall, ToolCallEnvelope } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { isObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { museItem } from '../protocol'
import { museNativeResult } from '../sourceData'
import { MUSE_TOOL_KINDS } from '../toolKinds'
import { museMcpSpec } from './mcp'
import { museTodoSpec } from './todo'
import { museItemLifecycle } from './toolCommon'

/**
 * One native tool item as a call.
 *
 * Every Muse item states its tool, its arguments and its status, so each row reads its
 * own item only. An unfinished item is the request row: it states no result and no
 * finished status. A finished item is the result row and builds the whole call.
 */
export function museToolCall(input: RowExtractionInput): ToolCall | null {
  const own = museItem(input.resolved.parentObject)
  if (!own)
    return null
  const id = pickString(own, 'itemId')
  const name = pickString(own, 'tool')
  const kind = MUSE_TOOL_KINDS.get(name) ?? 'other'
  let args: Record<string, unknown> = {}
  try {
    const parsed: unknown = JSON.parse(pickString(own, 'args'))
    if (isObject(parsed))
      args = parsed
  }
  catch {
    args = {}
  }
  const { nativeFinal, facts } = museItemLifecycle(own.status, input.completion ?? input.resolved.completion)
  const failed = facts.providerOutcome === 'failed'
  const declined = facts.providerOutcome === 'declined'
  const envelope: ToolCallEnvelope = {
    id,
    name,
    lifecycle: facts,
  }
  const preview = pickString(own, 'visibleOutput') || pickString(own, 'failureReason')
  const mcp = parseMcpToolName(name)
  if (mcp) {
    return createToolCall(envelope, museMcpSpec({
      ...mcp,
      args,
      nativeFinal,
      facts,
      status: pickString(own, 'status'),
      output: preview,
    }))
  }
  if (nativeFinal && facts.providerOutcome === null) {
    // Only the finished item states this status and this output, so they are facts of
    // the result.
    return createToolCall(envelope, {
      kind: 'other',
      request: { args },
      resultMetadata: [
        { label: 'Native status', value: pickString(own, 'status') || 'Unspecified' },
        ...(preview
          ? [
              { label: 'Native output', value: preview },
            ]
          : []),
      ],
    })
  }
  const native = museNativeResult(input.resolved.parentObject, input.resolved.supplementalContent)
  if (kind === 'execute' && !declined) {
    const structured = native?.structured
    const output = typeof args.command === 'string' && typeof structured?.chunk_id === 'string' && structured.chunk_id.trim() !== ''
      && structured.command === args.command && typeof structured.output === 'string'
      ? structured.output
      : preview
    const commandOutput = {
      output,
      ...(nativeFinal && !native
        ? { outputUnavailable: true }
        : {}),
    }
    const code = structured?.exit_code
    const signal = structured?.exit_signal
    const outcome: CommandResult = typeof code === 'number' && Number.isSafeInteger(code)
      ? { ...commandOutput, exitCode: code }
      : typeof signal === 'number' && Number.isSafeInteger(signal)
        ? {
            ...commandOutput,
            signal: String(signal),
          }
        : structured?.terminal_status === 'failed'
          ? { ...commandOutput, failed: true }
          : commandOutput
    return createToolCall(envelope, {
      kind: 'execute',
      request: DEFAULT_TOOL_REQUESTS.execute(args),
      label: 'Shell',
      ...(nativeFinal
        ? {
            result: {
              commands: [outcome],
              unresolvedTerminals: [],
            },
          }
        : {}),
    })
  }
  if (kind === 'todo') {
    return createToolCall(envelope, museTodoSpec({
      args,
      hasResult: facts.resultFrameLanded,
      failed,
      output: preview,
    }))
  }
  if (kind === 'read') {
    return createToolCall(envelope, {
      kind: 'read',
      request: DEFAULT_TOOL_REQUESTS.read(args),
      ...(nativeFinal
        ? {
            result: failed
              ? failedResult(preview)
              : { lines: null, fallbackContent: preview },
          }
        : {}),
    })
  }
  return createToolCall(envelope, {
    kind,
    request: DEFAULT_TOOL_REQUESTS[kind](args),
    ...(nativeFinal
      ? {
          result: failed || declined
            ? failedResult(preview)
            : unparsedResult(preview),
        }
      : {}),
  })
}

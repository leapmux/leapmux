import type { CommandResult } from '../../../model/commandResult'
import type { ToolCall, ToolCallEnvelope } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { isObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { museItem, museSiblingItem } from '../protocol'
import { museNativeResult } from '../sourceData'
import { MUSE_TOOL_KINDS } from '../toolKinds'
import { museMcpSpec } from './mcp'
import { museTodoSpec } from './todo'
import { museItemLifecycle } from './toolCommon'

export function museToolCall(input: RowExtractionInput): ToolCall | null {
  const own = museItem(input.resolved.parentObject)
  if (!own)
    return null
  const id = pickString(own, 'itemId')
  const sibling = museSiblingItem(input.resolved.parentObject, input.span.result?.parentObject)
  const resultItem = sibling ?? own
  const resultParse = sibling ? input.span.result : input.resolved
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
  const { nativeFinal, facts } = museItemLifecycle(resultItem.status, input.completion ?? input.resolved.completion)
  const failed = facts.providerOutcome === 'failed'
  const declined = facts.providerOutcome === 'declined'
  const envelope: ToolCallEnvelope = {
    id,
    name,
    lifecycle: facts,
  }
  const preview = pickString(resultItem, 'visibleOutput') || pickString(resultItem, 'failureReason')
  const mcp = parseMcpToolName(name)
  if (mcp) {
    return createToolCall(envelope, museMcpSpec({
      ...mcp,
      args,
      nativeFinal,
      facts,
      status: pickString(resultItem, 'status'),
      output: preview,
    }))
  }
  if (nativeFinal && facts.providerOutcome === null) {
    return createToolCall(envelope, {
      kind: 'other',
      request: { args },
      metadata: [{ label: 'Native status', value: pickString(resultItem, 'status') || 'Unspecified' }, ...(preview ? [{ label: 'Native output', value: preview }] : [])],
    })
  }
  const native = museNativeResult(resultParse?.parentObject, resultParse?.supplementalContent)
  if (kind === 'execute' && !declined) {
    const commandOutput = { output: preview, ...(nativeFinal && !native ? { outputUnavailable: true } : {}) }
    const structured = native?.structured
    const code = structured?.exit_code
    const signal = structured?.exit_signal
    const outcome: CommandResult = typeof code === 'number' && Number.isSafeInteger(code)
      ? { ...commandOutput, exitCode: code }
      : typeof signal === 'number' && Number.isSafeInteger(signal)
        ? { ...commandOutput, signal: String(signal) }
        : structured?.terminal_status === 'failed' ? { ...commandOutput, failed: true } : commandOutput
    return createToolCall(envelope, { kind: 'execute', request: DEFAULT_TOOL_REQUESTS.execute(args), label: 'Shell', ...(nativeFinal ? { result: { commands: [outcome], unresolvedTerminals: [] } } : {}) })
  }
  if (kind === 'todo') {
    return createToolCall(envelope, museTodoSpec({ args, hasResult: facts.resultFrameLanded, failed, output: preview }))
  }
  if (kind === 'read')
    return createToolCall(envelope, { kind: 'read', request: DEFAULT_TOOL_REQUESTS.read(args), ...(nativeFinal ? { result: failed ? failedResult(preview) : { lines: null, fallbackContent: preview } } : {}) })
  return createToolCall(envelope, { kind, request: DEFAULT_TOOL_REQUESTS[kind](args), ...(nativeFinal ? { result: failed || declined ? failedResult(preview) : unparsedResult(preview) } : {}) })
}

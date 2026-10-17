import type { CommandResult } from '../../../model/commandResult'
import type { ToolCall } from '../../../model/toolCall'
import type { CommandAction } from '../../../model/tools/execute'
import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEX_EXEC_OUTPUT, CODEX_ITEM, CODEX_ITEM_FIELD, CODEX_RAW_FIELD, CODEX_RAW_ITEM, CODEX_RAW_NAMESPACE, CODEX_RAW_TOOL, CODEX_SUPPLEMENT } from '~/generated/contracts/codex-protocol'
import { parseImageBlock } from '~/lib/imageBlocks'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { unparsedResult } from '../../../model/toolCall'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { CODEX_EXEC_CANCELLATION, CODEX_EXEC_CONTENT, CODEX_EXEC_DISPATCH_FAILURE, CODEX_EXEC_HEADER } from '../protocol'
import { extractItem } from './item'

export type CodexRawExecRole = 'request' | 'result'

/** Read the native call role. A generated request's completed status does not complete the script. */
export function codexRawExecRole(payload: Record<string, unknown> | undefined): CodexRawExecRole | null {
  const item = codexRawExecItem(payload)
  if (!item || !pickString(item, CODEX_RAW_FIELD.CallID))
    return null
  const type = item[CODEX_RAW_FIELD.Type]
  const namespace = item[CODEX_RAW_FIELD.Namespace]
  if (namespace !== undefined && namespace !== null && namespace !== '' && namespace !== CODEX_RAW_NAMESPACE.Functions)
    return null
  if (type === CODEX_RAW_ITEM.CustomToolCall)
    return item[CODEX_RAW_FIELD.Name] === CODEX_RAW_TOOL.Exec && typeof item[CODEX_RAW_FIELD.Input] === 'string' ? 'request' : null
  if (type === CODEX_RAW_ITEM.CustomToolCallOutput) {
    const name = item[CODEX_RAW_FIELD.Name]
    return name === undefined || name === null || name === '' || name === CODEX_RAW_TOOL.Exec ? 'result' : null
  }
  return null
}

/** Read the native wrapper without removing its thread and turn identities. */
function codexRawExecParameters(payload: Record<string, unknown>): Record<string, unknown> {
  return pickObject(payload, 'params') ?? payload
}

function codexRawExecItem(payload: Record<string, unknown> | undefined): Record<string, unknown> | null {
  if (!payload)
    return null
  return pickObject(codexRawExecParameters(payload), CODEX_RAW_FIELD.Item) ?? extractItem(payload)
}

/** Accept the counterpart only when its native call and available owner identities match. */
function matchingRawExecSide(payload: Record<string, unknown>, side: ParsedMessageContent | undefined, role: CodexRawExecRole): Record<string, unknown> | undefined {
  const candidate = side?.parentObject
  if (!candidate || codexRawExecRole(candidate) !== role)
    return undefined
  const own = codexRawExecItem(payload)
  const item = codexRawExecItem(candidate)
  if (!own || !item || item[CODEX_RAW_FIELD.CallID] !== own[CODEX_RAW_FIELD.CallID])
    return undefined
  const current = codexRawExecParameters(payload)
  const paired = codexRawExecParameters(candidate)
  for (const field of [CODEX_RAW_FIELD.ThreadID, CODEX_RAW_FIELD.TurnID]) {
    if (current[field] !== undefined && paired[field] !== undefined && current[field] !== paired[field])
      return undefined
  }
  return item
}

/** How one output of a code-mode cell ends: the status line of its header. */
type CodexExecStatus = 'completed' | 'failed' | 'terminated' | 'running'

/** One output of a code-mode cell: the exec output, or one wait output after a yield. */
interface CodexExecSegment {
  /** The status that the header states, or null when the header is not a native header. */
  status: CodexExecStatus | null
  /** The text of every text block, the header included. */
  text: string[]
  /** The script output after the header. */
  body: string[]
  images: ImageResultSource[]
  durationMs?: number
}

const CODEX_EXEC_TIMING = /^Wall time (\d+(?:\.\d+)?) seconds(?: \(code-mode [\d.]+ seconds; overhead -?[\d.]+ seconds\))?\nOutput:\n/

/** The status that one header line states, or null for a line that no native header starts with. */
function codexExecStatus(firstLine: string): CodexExecStatus | null {
  switch (firstLine) {
    case CODEX_EXEC_OUTPUT.CompletedHeader:
      return 'completed'
    case CODEX_EXEC_HEADER.Failed:
      return 'failed'
    case CODEX_EXEC_HEADER.Terminated:
      return 'terminated'
    default:
      return firstLine.startsWith(CODEX_EXEC_OUTPUT.RunningHeaderPrefix) && firstLine.length > CODEX_EXEC_OUTPUT.RunningHeaderPrefix.length
        ? 'running'
        : null
  }
}

/** Read the native header before script text. A status line that the script prints cannot change the outcome. */
function codexExecSegment(output: unknown): CodexExecSegment | null {
  if (typeof output === 'string') {
    if (output === CODEX_EXEC_DISPATCH_FAILURE)
      return { status: 'failed', text: [output], body: [output], images: [] }
    const cancellation = CODEX_EXEC_CANCELLATION.exec(output)
    const seconds = cancellation && cancellation[0] === output ? Number(cancellation[1]) : Number.NaN
    const durationMs = seconds * 1000
    // The native producer applies a 0.1-second minimum to elapsed time.
    // A script's text block cannot supply this provider outcome.
    if (seconds >= 0.1 && Number.isFinite(durationMs))
      return { status: 'terminated', text: [output], body: [output], images: [], durationMs }
  }
  const blocks: unknown[] | null = typeof output === 'string' ? [{ type: CODEX_EXEC_OUTPUT.TextContent, text: output }] : Array.isArray(output) ? output : null
  if (!blocks)
    return null
  const text: string[] = []
  const images: ImageResultSource[] = []
  for (const block of blocks) {
    if (!isObject(block))
      continue
    if (block.type === CODEX_EXEC_OUTPUT.TextContent && typeof block.text === 'string') {
      text.push(block.text)
    }
    else if (block.type === CODEX_EXEC_CONTENT.Image) {
      const url = pickString(block, CODEX_EXEC_CONTENT.ImageURL)
      const image = url ? parseImageBlock({ type: 'image', url }) : null
      if (image)
        images.push(image)
    }
  }
  const first = blocks[0]
  const header = isObject(first) && first.type === CODEX_EXEC_OUTPUT.TextContent && typeof first.text === 'string' ? first.text : ''
  const timing = CODEX_EXEC_TIMING.exec(header.slice(header.indexOf('\n') + 1))
  const status = timing === null ? null : codexExecStatus(header.split('\n', 1)[0] ?? '')
  const seconds = timing?.[1] === undefined ? undefined : Number(timing[1])
  let body = text
  if (status !== null && timing) {
    const remainder = header.slice(header.indexOf('\n') + 1 + timing[0].length)
    body = remainder === '' ? text.slice(1) : [remainder, ...text.slice(1)]
  }
  return {
    status,
    text,
    body,
    images,
    ...(seconds !== undefined && Number.isFinite(seconds) ? { durationMs: seconds * 1000 } : {}),
  }
}

/** Every output of a code-mode cell, joined into what one card states. */
interface CodexRawExecOutput {
  text: string
  images: ImageResultSource[]
  /** The status of the last output, or null when a header is not a native header. */
  status: CodexExecStatus | null
  durationMs?: number
}

/**
 * Read every output of a code-mode cell, in order.
 *
 * A cell that yields keeps running, and the model reads the rest of its output through
 * `wait` calls. The worker stores each wait output beside the exec output, and the
 * resolve puts them on the item under `continuationOutputs` (see resolveMessage.ts).
 * Each yield returns only the output since the yield before it, so the card joins
 * every body, and the last header states how the cell ended. A continuation value
 * that is neither text nor a list holds no output, so the card skips it.
 *
 * The status and the duration belong to the model fields. The command body and the
 * Copy action contain script output only. A cell that ran across several calls states
 * no duration, because no one wall time covers the cell.
 */
function codexRawExecOutput(item: Record<string, unknown>): CodexRawExecOutput | null {
  const first = codexExecSegment(item[CODEX_RAW_FIELD.Output])
  if (!first)
    return null
  const continuation = item[CODEX_SUPPLEMENT.ContinuationOutputs]
  const later = Array.isArray(continuation) ? continuation.map(codexExecSegment).filter(segment => segment !== null) : []
  const segments = [first, ...later]
  const last = segments[segments.length - 1]!
  const images = segments.flatMap(segment => segment.images)
  if (segments.some(segment => segment.status === null))
    return { text: segments.flatMap(segment => segment.text).join('\n'), images, status: null }
  // A yield that printed nothing adds no empty line between the bodies around it.
  const body = segments.flatMap(segment => segment.status === 'running' ? segment.body.filter(text => text !== '') : segment.body)
  return {
    text: body.join('\n'),
    images,
    status: last.status,
    ...(segments.length === 1 && last.durationMs !== undefined ? { durationMs: last.durationMs } : {}),
  }
}

/** Build one native JavaScript execution through the existing neutral execute model. */
export function codexRawExecCall(payload: Record<string, unknown>, sides: ToolSpanContext, completion?: MessageCompletion): { call: ToolCall<'execute'>, role: CodexRawExecRole } | null {
  const ownRole = codexRawExecRole(payload)
  const own = codexRawExecItem(payload)
  if (!ownRole || !own)
    return null
  const request = ownRole === 'request' ? own : matchingRawExecSide(payload, sides.request, 'request')
  const result = ownRole === 'result' ? own : matchingRawExecSide(payload, sides.result, 'result')
  const output = result ? codexRawExecOutput(result) : null
  const retained = retainedRowIsFinal(completion)
  const ended = ownRole === 'result' || retained
  const call = createToolCall({
    id: pickString(own, CODEX_RAW_FIELD.CallID),
    name: CODEX_RAW_TOOL.Exec,
    lifecycle: {
      frameStatus: result ? 'unstated' : 'in_progress',
      providerOutcome: output?.status === 'failed' ? 'failed' : output?.status === 'terminated' ? 'interrupted' : null,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: ended,
      resultFrameLanded: result !== undefined,
    },
  }, {
    kind: 'execute',
    label: 'Code execution',
    request: { command: pickString(request, CODEX_RAW_FIELD.Input), language: 'javascript' },
    ...(output
      ? {
          result: output.status !== null
            ? { commands: [{ output: output.text, ...(output.status === 'failed' ? { failed: true as const } : {}), ...(output.durationMs !== undefined ? { durationMs: output.durationMs } : {}) }], unresolvedTerminals: [] }
            : unparsedResult(output.text),
          images: output.images,
        }
      : {}),
  })
  return call.kind === 'execute' ? { call, role: retained ? 'result' : ownRole } : null
}

/** Regex to strip shell wrappers like `/bin/zsh -lc '...'` from commands. */
const SHELL_WRAPPER_RE = /^\/bin\/(?:ba|z)?sh\s+-lc\s+'(.+)'$/

/** Extract command output and status. Return null for other item types. */
export function codexCommandFromItem(item: Record<string, unknown> | null | undefined): CommandResult | null {
  if (!item || item.type !== CODEX_ITEM.CommandExecution)
    return null

  const exitCode = pickNumber(item, 'exitCode')
  return {
    output: pickString(item, CODEX_ITEM_FIELD.AggregatedOutput),
    exitCode,
    durationMs: pickNumber(item, 'durationMs'),
  }
}

/** Strip a shell wrapper like `/bin/zsh -lc '...'` to surface the actual command. */
export function codexUnwrapCommand(rawCommand: string): string {
  return rawCommand.replace(SHELL_WRAPPER_RE, '$1')
}

/** Translate Codex's best-effort command breakdown into the shared execute model. */
export function codexCommandActionsFromItem(item: Record<string, unknown>): CommandAction[] {
  const rawActions: unknown[] = Array.isArray(item.commandActions) ? item.commandActions : []
  const actions: CommandAction[] = []

  for (const value of rawActions) {
    if (!isObject(value))
      continue
    const command = pickString(value, 'command')
    if (!command)
      continue

    switch (pickString(value, 'type')) {
      case 'read': {
        const name = pickString(value, 'name')
        const path = pickString(value, 'path')
        actions.push(name && path
          ? { kind: 'read', command, name, path }
          : { kind: 'unknown', command })
        break
      }
      case 'listFiles': {
        const path = pickString(value, 'path') || undefined
        actions.push({ kind: 'list', command, ...(path !== undefined ? { path } : {}) })
        break
      }
      case 'search': {
        const query = pickString(value, 'query') || undefined
        const path = pickString(value, 'path') || undefined
        actions.push({
          kind: 'search',
          command,
          ...(query !== undefined ? { query } : {}),
          ...(path !== undefined ? { path } : {}),
        })
        break
      }
      default:
        // Preserve explicit `unknown` actions and future Codex variants. The raw
        // command remains useful even when this client cannot describe the action.
        actions.push({ kind: 'unknown', command })
    }
  }

  return actions
}

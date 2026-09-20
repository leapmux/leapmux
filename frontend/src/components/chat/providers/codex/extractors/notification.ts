import type { CompactionDetails, CompactionPhase, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEX_ITEM, CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { compactionMetaFromBoundary } from '../../../model/notification'
import { codexHookIsFailureOrUnknown } from '../hookNotifications'
import { codexMcpOauthIsFailureOrUnknown, codexMcpStartupIsFailureOrUnknown } from '../mcpNotifications'
import { CODEX_RATE_LIMITS_METHOD, codexRateLimitEntries } from '../rateLimits'

const STARTUP_METHOD = CODEX_METHOD.McpServerStartupStatusUpdated

type StartupKind = 'failed' | 'unknown'

interface ParsedMcpStartup {
  kind: StartupKind
  rawState: string
  name: string
  errorSuffix: string
}

function startupStateAndError(status: unknown, fallbackError: unknown): { state: string, error: string } {
  const fallback = typeof fallbackError === 'string' ? fallbackError : ''
  if (typeof status === 'string')
    return { state: status, error: fallback }
  if (isObject(status)) {
    return {
      state: pickString(status, 'state'),
      error: pickString(status, 'error') || fallback,
    }
  }
  return { state: '', error: fallback }
}

function parseMcpStartup(parsed: Record<string, unknown>): ParsedMcpStartup | null {
  if (parsed.method !== STARTUP_METHOD)
    return null
  const params = pickObject(parsed, 'params')
  const name = pickString(params, 'name').trim()
  const { state, error } = startupStateAndError(params?.status, params?.error)
  const rawState = state.trim()
  const errorSuffix = error.trim() ? ` (${error.trim()})` : ''
  const kind: StartupKind = rawState === 'failed' ? 'failed' : 'unknown'
  return { kind, rawState, name, errorSuffix }
}

function startupGroupEntry(parsed: Record<string, unknown>): NotificationEntry | null {
  if (!codexMcpStartupIsFailureOrUnknown(parsed))
    return null
  const p = parseMcpStartup(parsed)
  if (!p)
    return null
  const stateLabel = p.rawState || 'unknown'
  const prefix = p.kind === 'unknown'
    ? `MCP server status update (${stateLabel})`
    : 'MCP server failed to start'
  // A name-less startup has no server to group under the prefix, so render the
  // prefix alone as a plain line rather than grouping a placeholder beneath it.
  if (!p.name)
    return { kind: 'text', text: `${prefix}${p.errorSuffix}` }
  const groupKey = p.kind === 'unknown' ? `status:${stateLabel}` : p.kind
  return { kind: 'group', groupKey, prefix, entry: `${p.name}${p.errorSuffix}` }
}

function mcpOauthFailureEntry(parsed: Record<string, unknown>): NotificationEntry | null {
  if (!codexMcpOauthIsFailureOrUnknown(parsed))
    return null
  const params = pickObject(parsed, 'params')
  const name = pickString(params, 'name').trim()
  const error = pickString(params, 'error').trim()
  const outcome = params?.success === false ? 'failed' : 'status unknown'
  const subject = name ? ` for ${name}` : ''
  const detail = error ? `: ${error}` : ''
  return { kind: 'status', text: `MCP OAuth login ${outcome}${subject}${detail}` }
}

function hookFailureEntry(parsed: Record<string, unknown>): NotificationEntry | null {
  if (!codexHookIsFailureOrUnknown(parsed))
    return null
  const run = pickObject(pickObject(parsed, 'params'), 'run')
  const status = pickString(run, 'status') || 'status unknown'
  const entries = Array.isArray(run?.entries) ? run.entries : []
  const entryText = entries
    .map(entry => pickString(isObject(entry) ? entry : undefined, 'text').trim())
    .filter(Boolean)
    .join('; ')
  const detail = entryText || pickString(run, 'statusMessage').trim()
  return { kind: 'status', text: `Hook ${status}${detail ? `: ${detail}` : ''}` }
}

/**
 * The context-compaction item Codex reports, in either of its two envelopes.
 *
 * A `contextCompaction` item arrives at the top level of a persisted row and inside
 * the raw `item/started` or `item/completed` JSON-RPC notification, so both are read.
 * `started` is the boundary in progress; `completed` is the boundary itself.
 */
function codexCompactionPhase(m: Record<string, unknown>): CompactionPhase | null {
  // A row an older worker synthesized carries Claude's `compact_boundary` shape,
  // because the worker used to normalize every provider's boundary into it. Those
  // rows are persisted, so Codex still reads its own history.
  if (m.type === 'system' && m.subtype === 'compact_boundary')
    return 'end'
  const item = pickObject(m, 'item') ?? pickObject(pickObject(m, 'params'), 'item')
  if (item?.type !== CODEX_ITEM.ContextCompaction)
    return null
  if (m.method === CODEX_METHOD.ItemStarted)
    return 'start'
  return 'end'
}

/**
 * The compaction boundary a Codex message states, or null when it states none.
 *
 * A sibling of Claude's, and it serves the same two readers: the context-usage grid
 * outside the render tree, and the notification extractor below.
 */
export function codexCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const inner = getInnerMessage(parsed)
  if (!isObject(inner) || codexCompactionPhase(inner) !== 'end')
    return null
  return compactionMetaFromBoundary(inner)
}

/**
 * Read one Codex notification frame into the shared notification model.
 *
 * Returns an empty array for a frame Codex recognizes and suppresses, and for one it
 * does not recognize at all -- a notification that produced no entry draws nothing,
 * which is the safe answer either way.
 */
export function codexNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  if (msg.method === CODEX_METHOD.SkillsChanged || msg.method === CODEX_METHOD.RemoteControlStatusChanged)
    return []

  const oauthFailure = mcpOauthFailureEntry(msg)
  if (oauthFailure)
    return [oauthFailure]

  const startup = startupGroupEntry(msg)
  if (startup)
    return [startup]

  const hookFailure = hookFailureEntry(msg)
  if (hookFailure)
    return [hookFailure]

  if (msg.method === CODEX_RATE_LIMITS_METHOD)
    return codexRateLimitEntries(msg)

  const phase = codexCompactionPhase(msg)
  if (phase) {
    const detail = phase === 'end' ? compactionMetaFromBoundary(msg) : null
    return [{ kind: 'compaction', phase, ...(detail !== null ? { detail } : {}) }]
  }

  // An `error` frame states whether Codex intends to try again, which is the
  // difference between a stall a reader waits out and one they must act on.
  if (msg.method === CODEX_METHOD.Error) {
    const error = pickObject(msg, 'params') ?? msg
    const detail = pickObject(error, 'error') ?? error
    const message = pickString(detail, 'message')
    const info = pickString(detail, 'codexErrorInfo')
    const errorText = [message, info].filter(Boolean).join(' — ') || undefined
    return [{
      kind: 'retry',
      scope: 'api',
      willRetry: error.willRetry === true,
      ...(errorText !== undefined ? { error: errorText } : {}),
    }]
  }
  if (msg.method === CODEX_METHOD.Warning) {
    const params = pickObject(msg, 'params') ?? msg
    const text = pickString(params, 'message') || pickString(pickObject(params, 'warning'), 'message')
    return text ? [{ kind: 'status', text: `Warning: ${text}` }] : []
  }

  return []
}

import type { AutoReviewOutcome, AutoReviewRisk, CompactionDetails, CompactionPhase, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEX_AUTO_REVIEW_ACTION, CODEX_AUTO_REVIEW_STATUS, CODEX_ITEM, CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { compactionMetaFromBoundary } from '../../../model/notification'
import { codexHookIsFailureOrUnknown } from '../hookNotifications'
import { codexMcpOauthIsFailureOrUnknown, codexMcpStartupIsFailureOrUnknown } from '../mcpNotifications'

const STARTUP_METHOD = CODEX_METHOD.McpServerStartupStatusUpdated

/** The `model/verification` flag for which the Codex TUI states its notice. */
const CODEX_TRUSTED_ACCESS_FOR_CYBER = 'trustedAccessForCyber'

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
  if (m.type === 'system' && m.subtype === 'status' && m.status === 'compacting')
    return 'start'
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

const STRICT_REVIEW_NOTICE = 'This request needs extra safety checks, which can take more time.'

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
  if (msg.method === CODEX_METHOD.ConfigWarning) {
    const params = pickObject(msg, 'params') ?? msg
    const summary = pickString(params, 'summary').trim()
    const details = pickString(params, 'details').trim()
    const path = pickString(params, 'path').trim()
    if (!summary)
      return []
    const text = `Warning: ${summary}${details ? `: ${details}` : ''}${path ? ` (${path})` : ''}`
    return [{ kind: 'status', text }]
  }
  if (msg.method === CODEX_METHOD.DeprecationNotice) {
    const params = pickObject(msg, 'params') ?? msg
    const summary = pickString(params, 'summary').trim()
    const details = pickString(params, 'details').trim()
    if (!summary)
      return []
    return [{ kind: 'status', text: `Deprecated: ${summary}${details ? ` — ${details}` : ''}` }]
  }
  if (msg.method === CODEX_METHOD.ModelSafetyBufferingUpdated) {
    return [{ kind: 'text', text: 'Giving this request a little extra thought' }]
  }
  if (msg.method === CODEX_METHOD.ModelVerification) {
    // The Codex TUI states this notice only for the trustedAccessForCyber flag.
    // The row draws plain text, so the URL carries no markdown brackets.
    const params = pickObject(msg, 'params') ?? msg
    const verifications = Array.isArray(params.verifications) ? params.verifications : []
    if (!verifications.includes(CODEX_TRUSTED_ACCESS_FOR_CYBER))
      return []
    return [{
      kind: 'text',
      text: 'Your conversations have multiple flags for possible cybersecurity risk. Responses may take longer because extra safety checks are on. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber',
    }]
  }
  if (msg.method === CODEX_METHOD.AuthRecoveryStarted || msg.method === CODEX_METHOD.AuthRecoveryCompleted) {
    const params = pickObject(msg, 'params') ?? msg
    const text = pickString(params, 'message').trim()
    return text ? [{ kind: 'text', text }] : []
  }

  if (msg.method === CODEX_METHOD.AutoApprovalReviewCompleted) {
    const review = codexAutoReviewEntry(pickObject(msg, 'params'))
    return review ? [review] : []
  }
  // The worker keeps only the warning that states why auto-review stopped a turn,
  // so its own words carry the reason.
  if (msg.method === CODEX_METHOD.GuardianWarning) {
    const text = pickString(pickObject(msg, 'params'), 'message').trim()
    return text ? [{ kind: 'status', text }] : []
  }
  if (msg.method === CODEX_METHOD.StrictReviewRequired)
    return [{ kind: 'status', text: STRICT_REVIEW_NOTICE }]

  return []
}

// A map, not an object literal: the status is a wire word, and a word that spells an
// `Object.prototype` member would read an inherited function from an object.
const AUTO_REVIEW_OUTCOMES: ReadonlyMap<string, AutoReviewOutcome> = new Map([
  [CODEX_AUTO_REVIEW_STATUS.Approved, 'approved'],
  [CODEX_AUTO_REVIEW_STATUS.Denied, 'denied'],
  [CODEX_AUTO_REVIEW_STATUS.TimedOut, 'timed-out'],
  [CODEX_AUTO_REVIEW_STATUS.Aborted, 'aborted'],
])

const AUTO_REVIEW_RISKS: readonly AutoReviewRisk[] = ['low', 'medium', 'high', 'critical']

/** The member of values that equals value, or undefined for a word the model does not know. */
function oneOf<T extends string>(values: readonly T[], value: string): T | undefined {
  return values.find(candidate => candidate === value)
}

/**
 * Read a completed Codex auto-review into the neutral review entry. A frame with no
 * review id draws nothing, because the reader could not approve it and no row can
 * name it.
 *
 * Only a denial in the main thread is approvable: Codex refuses the approval for a
 * Multi-Agent V2 subagent, whose reviews the worker routes to the child transcript.
 * A review in the main thread is the only one that reaches the main transcript, so
 * every denial that this reader sees there is approvable. The child tab offers no
 * approval capability, which hides the control for its reviews.
 */
function codexAutoReviewEntry(params: Record<string, unknown> | null | undefined): NotificationEntry | null {
  const reviewId = pickString(params, 'reviewId')
  if (!reviewId)
    return null
  const review = pickObject(params, 'review')
  const outcome = AUTO_REVIEW_OUTCOMES.get(pickString(review, 'status')) ?? 'unknown'
  const risk = oneOf(AUTO_REVIEW_RISKS, pickString(review, 'riskLevel'))
  const rationale = pickString(review, 'rationale').trim()
  return {
    kind: 'auto-review',
    reviewId,
    outcome,
    ...(risk !== undefined ? { risk } : {}),
    ...(rationale ? { rationale } : {}),
    action: codexAutoReviewAction(pickObject(params, 'action')),
    approvable: outcome === 'denied',
  }
}

/** A one-line summary of the action that a Codex auto-review judged. */
export function codexAutoReviewAction(action: Record<string, unknown> | null | undefined): string {
  switch (pickString(action, 'type')) {
    case CODEX_AUTO_REVIEW_ACTION.Command:
      return pickString(action, 'command')
    case CODEX_AUTO_REVIEW_ACTION.Execve: {
      const argv = Array.isArray(action?.argv) ? action.argv.filter((part): part is string => typeof part === 'string') : []
      return argv.length > 0 ? argv.map(quoteShellWord).join(' ') : pickString(action, 'program')
    }
    case CODEX_AUTO_REVIEW_ACTION.WriteStdin:
      return `Write to process ${pickString(action, 'processId')}`
    case CODEX_AUTO_REVIEW_ACTION.ApplyPatch: {
      const files = Array.isArray(action?.files) ? action.files.filter((file): file is string => typeof file === 'string') : []
      return files.length > 0 ? `Edit ${files.join(', ')}` : 'Edit files'
    }
    case CODEX_AUTO_REVIEW_ACTION.NetworkAccess: {
      const host = pickString(action, 'host')
      const port = typeof action?.port === 'number' ? action.port : undefined
      return `Connect to ${port !== undefined ? `${host}:${port}` : host}`
    }
    case CODEX_AUTO_REVIEW_ACTION.McpToolCall: {
      const tool = pickString(action, 'toolTitle') || pickString(action, 'toolName')
      return [pickString(action, 'server'), tool].filter(Boolean).join(' · ')
    }
    case CODEX_AUTO_REVIEW_ACTION.RequestPermissions:
      return pickString(action, 'reason').trim() || 'Request permissions'
    default:
      return 'An action'
  }
}

/** Quote one shell word only when it needs quoting, so a plain argv reads plainly. */
function quoteShellWord(word: string): string {
  return /^[\w./:=@%+,-]+$/.test(word) ? word : `'${word.replaceAll('\'', `'\\''`)}'`
}

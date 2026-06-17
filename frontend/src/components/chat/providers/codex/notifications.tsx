import type { NotificationThreadEntry } from '../registry'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_RATE_LIMITS_METHOD, codexRateLimitReachedType, formatCodexRateLimitReached, formatRateLimitMessage, iterCodexRateLimitTiers } from '~/lib/rateLimitUtils'
import { CODEX_METHOD } from '~/types/toolMessages'

const STARTUP_METHOD = CODEX_METHOD.MCP_SERVER_STARTUP_STATUS_UPDATED

type StartupKind = 'starting' | 'ready' | 'failed' | 'cancelled' | 'unknown'

const STARTUP_KIND_PREFIX: Record<Exclude<StartupKind, 'unknown'>, string> = {
  starting: 'Starting MCP server',
  ready: 'MCP server ready',
  failed: 'MCP server failed to start',
  cancelled: 'MCP server startup cancelled',
}

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
  const kind: StartupKind = rawState in STARTUP_KIND_PREFIX
    ? (rawState as Exclude<StartupKind, 'unknown'>)
    : 'unknown'
  return { kind, rawState, name, errorSuffix }
}

// `failed`/`cancelled`/`unknown` carry the error suffix into the rendered
// string; `starting`/`ready` do not (they aren't error states).
function appendsSuffix(kind: StartupKind): boolean {
  return kind !== 'starting' && kind !== 'ready'
}

function startupGroupEntry(parsed: Record<string, unknown>): NotificationThreadEntry | null {
  const p = parseMcpStartup(parsed)
  if (!p)
    return null
  const suffix = appendsSuffix(p.kind) ? p.errorSuffix : ''
  const stateLabel = p.rawState || 'unknown'
  const prefix = p.kind === 'unknown'
    ? `MCP server status update (${stateLabel})`
    : STARTUP_KIND_PREFIX[p.kind]
  // A name-less startup has no server to group under the prefix, so render the
  // prefix alone as a plain line (e.g. "MCP server ready") rather than grouping
  // a placeholder "unknown" beneath it.
  if (!p.name)
    return { kind: 'text', text: `${prefix}${suffix}` }
  const groupKey = p.kind === 'unknown' ? `status:${stateLabel}` : p.kind
  return { kind: 'group', groupKey, prefix, entry: `${p.name}${suffix}` }
}

/**
 * Convert one Codex notification message into a thread entry, for
 * `renderNotificationThread`. Returns null for messages this provider doesn't
 * recognize, letting the shared switch handle them.
 */
export function codexNotificationThreadEntry(msg: Record<string, unknown>): NotificationThreadEntry[] | null {
  if (msg.method === CODEX_METHOD.SKILLS_CHANGED || msg.method === CODEX_METHOD.REMOTE_CONTROL_STATUS_CHANGED)
    return []

  const startup = startupGroupEntry(msg)
  if (startup)
    return [startup]

  if (msg.method === CODEX_RATE_LIMITS_METHOD) {
    const entries: NotificationThreadEntry[] = []
    for (const { info } of iterCodexRateLimitTiers(msg)) {
      if (info.rateLimitType && info.status !== 'allowed')
        entries.push({ kind: 'text', text: formatRateLimitMessage(info) })
    }
    // A snapshot-level reached-type (credits depleted, usage cap, or a rate
    // limit whose window rounded under threshold) is an authoritative block even
    // when no per-tier window is over its threshold. Surface it so the block is
    // never silently hidden; skip when a tier line already conveys the throttle.
    const reached = codexRateLimitReachedType(msg)
    if (reached && entries.length === 0)
      entries.push({ kind: 'text', text: formatCodexRateLimitReached(reached) })
    return entries
  }

  return null
}

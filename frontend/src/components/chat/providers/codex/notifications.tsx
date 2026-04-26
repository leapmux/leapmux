import type { JSX, JSXElement } from 'solid-js'
import type { RenderContext } from '../../messageRenderers'
import type { NotificationThreadEntry } from '../registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { CODEX_RATE_LIMITS_METHOD, formatRateLimitMessage, iterCodexRateLimitTiers } from '~/lib/rateLimitUtils'
import { CODEX_METHOD } from '~/types/toolMessages'
import { controlResponseMessage } from '../../messageStyles.css'

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

function formatStartupStatus(parsed: Record<string, unknown>): string | null {
  const p = parseMcpStartup(parsed)
  if (!p)
    return null
  const suffix = appendsSuffix(p.kind) ? p.errorSuffix : ''
  if (p.kind === 'unknown') {
    const stateLabel = p.rawState || 'unknown'
    return p.name
      ? `MCP server status update: ${p.name} (${stateLabel})${suffix}`
      : `MCP server status update (${stateLabel})${suffix}`
  }
  const prefix = STARTUP_KIND_PREFIX[p.kind]
  return p.name ? `${prefix}: ${p.name}${suffix}` : `${prefix}${suffix}`
}

function startupGroupEntry(parsed: Record<string, unknown>): NotificationThreadEntry | null {
  const p = parseMcpStartup(parsed)
  if (!p)
    return null
  const suffix = appendsSuffix(p.kind) ? p.errorSuffix : ''
  const entry = `${p.name || 'unknown'}${suffix}`
  if (p.kind === 'unknown') {
    const stateLabel = p.rawState || 'unknown'
    return { kind: 'group', groupKey: `status:${stateLabel}`, prefix: `MCP server status update (${stateLabel})`, entry }
  }
  return { kind: 'group', groupKey: p.kind, prefix: STARTUP_KIND_PREFIX[p.kind], entry }
}

/** Render a Codex `account/rateLimits/updated` notification. */
function renderRateLimits(parsed: Record<string, unknown>): JSXElement {
  const parts: string[] = []
  let sawAnyTier = false
  for (const { info } of iterCodexRateLimitTiers(parsed)) {
    sawAnyTier = true
    if (info.status === 'allowed')
      continue
    parts.push(formatRateLimitMessage(info))
  }
  if (!sawAnyTier)
    return <div class={controlResponseMessage}>Rate limit update</div>
  if (parts.length === 0)
    return null
  return <div class={controlResponseMessage}>{parts.join(', ')}</div>
}

/**
 * Render a Codex notification message. Handles `mcpServer/startupStatus/updated`
 * (MCP server lifecycle) and `account/rateLimits/updated`. Returns null for
 * other shapes — the Codex plugin's `renderMessage` falls through to the
 * Claude-shaped renderers for `settings_changed` etc.
 */
export function codexNotificationRenderer(
  parsed: unknown,
  _role: MessageRole,
  _context?: RenderContext,
): JSX.Element | null {
  if (!isObject(parsed))
    return null
  const startupLabel = formatStartupStatus(parsed)
  if (startupLabel)
    return <div class={controlResponseMessage}>{startupLabel}</div>
  if (parsed.method === CODEX_RATE_LIMITS_METHOD)
    return renderRateLimits(parsed)
  return null
}

/**
 * Convert one Codex notification message into a thread entry, for
 * `renderNotificationThread`. Returns null for messages this provider doesn't
 * recognize, letting the shared switch handle them.
 */
export function codexNotificationThreadEntry(msg: Record<string, unknown>): NotificationThreadEntry[] | null {
  const startup = startupGroupEntry(msg)
  if (startup)
    return [startup]

  if (msg.method === CODEX_RATE_LIMITS_METHOD) {
    const entries: NotificationThreadEntry[] = []
    for (const { info } of iterCodexRateLimitTiers(msg)) {
      if (info.rateLimitType && info.status !== 'allowed')
        entries.push({ kind: 'text', text: formatRateLimitMessage(info) })
    }
    return entries
  }

  return null
}

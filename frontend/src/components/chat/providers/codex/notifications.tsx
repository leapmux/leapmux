import type { JSX, JSXElement } from 'solid-js'
import type { RenderContext } from '../../messageRenderers'
import type { NotificationThreadEntry } from '../registry'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { codexTierToRateLimitInfo, formatRateLimitMessage } from '~/lib/rateLimitUtils'
import { controlResponseMessage } from '../../messageStyles.css'

const STARTUP_METHOD = 'mcpServer/startupStatus/updated'
const RATE_LIMITS_METHOD = 'account/rateLimits/updated'

function startupStateAndError(status: unknown, fallbackError: unknown): { state: string, error: string } {
  if (typeof status === 'string') {
    return {
      state: status,
      error: typeof fallbackError === 'string' ? fallbackError : '',
    }
  }

  if (isObject(status)) {
    return {
      state: typeof status.state === 'string' ? status.state : '',
      error: typeof status.error === 'string' ? status.error : typeof fallbackError === 'string' ? fallbackError : '',
    }
  }

  return {
    state: '',
    error: typeof fallbackError === 'string' ? fallbackError : '',
  }
}

function formatStartupStatus(parsed: Record<string, unknown>): string | null {
  if (parsed.method !== STARTUP_METHOD)
    return null

  const params = isObject(parsed.params) ? parsed.params as Record<string, unknown> : undefined
  const name = pickString(params, 'name').trim()
  const { state, error } = startupStateAndError(params?.status, params?.error)
  const normalizedState = state.trim()
  const normalizedError = error.trim()
  const suffix = normalizedError ? ` (${normalizedError})` : ''

  switch (normalizedState) {
    case 'starting':
      return name ? `Starting MCP server: ${name}` : 'Starting MCP server'
    case 'ready':
      return name ? `MCP server ready: ${name}` : 'MCP server ready'
    case 'failed':
      return name ? `MCP server failed to start: ${name}${suffix}` : `MCP server failed to start${suffix}`
    case 'cancelled':
      return name ? `MCP server startup cancelled: ${name}${suffix}` : `MCP server startup cancelled${suffix}`
    default: {
      const stateLabel = normalizedState || 'unknown'
      if (name)
        return `MCP server status update: ${name} (${stateLabel})${suffix}`
      return `MCP server status update (${stateLabel})${suffix}`
    }
  }
}

function startupGroupEntry(parsed: Record<string, unknown>): NotificationThreadEntry | null {
  if (parsed.method !== STARTUP_METHOD)
    return null

  const params = isObject(parsed.params) ? parsed.params as Record<string, unknown> : undefined
  const name = pickString(params, 'name').trim()
  const { state, error } = startupStateAndError(params?.status, params?.error)
  const normalizedState = state.trim() || 'unknown'
  const normalizedError = error.trim()
  const baseName = name || 'unknown'
  const errorSuffix = normalizedError ? ` (${normalizedError})` : ''

  switch (normalizedState) {
    case 'starting':
      return { kind: 'group', groupKey: 'starting', prefix: 'Starting MCP server', entry: baseName }
    case 'ready':
      return { kind: 'group', groupKey: 'ready', prefix: 'MCP server ready', entry: baseName }
    case 'failed':
      return { kind: 'group', groupKey: 'failed', prefix: 'MCP server failed to start', entry: `${baseName}${errorSuffix}` }
    case 'cancelled':
      return { kind: 'group', groupKey: 'cancelled', prefix: 'MCP server startup cancelled', entry: `${baseName}${errorSuffix}` }
    default:
      return { kind: 'group', groupKey: `status:${normalizedState}`, prefix: `MCP server status update (${normalizedState})`, entry: `${baseName}${errorSuffix}` }
  }
}

/** Render a Codex `account/rateLimits/updated` notification. */
function renderRateLimits(parsed: Record<string, unknown>): JSXElement {
  const params = parsed.params as Record<string, unknown> | undefined
  const rl = params?.rateLimits as Record<string, unknown> | undefined
  if (!rl)
    return <div class={controlResponseMessage}>Rate limit update</div>

  const parts: string[] = []
  for (const tierKey of ['primary', 'secondary'] as const) {
    const tier = rl[tierKey] as Record<string, unknown> | undefined
    if (!tier)
      continue
    const info = codexTierToRateLimitInfo(tier)
    if (info.status === 'allowed')
      continue
    parts.push(formatRateLimitMessage(info))
  }
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
  if (parsed.method === RATE_LIMITS_METHOD)
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

  if (msg.method === RATE_LIMITS_METHOD) {
    const params = msg.params as Record<string, unknown> | undefined
    const rl = params?.rateLimits as Record<string, unknown> | undefined
    if (!rl)
      return []
    const entries: NotificationThreadEntry[] = []
    for (const tierKey of ['primary', 'secondary'] as const) {
      const tier = rl[tierKey] as Record<string, unknown> | undefined
      if (!tier)
        continue
      const info = codexTierToRateLimitInfo(tier)
      if (info.rateLimitType && info.status !== 'allowed')
        entries.push({ kind: 'text', text: formatRateLimitMessage(info) })
    }
    return entries
  }

  return null
}

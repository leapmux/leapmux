/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSXElement } from 'solid-js'
import type { MessageContentRenderer } from './messageRenderers'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Icon } from '~/components/common/Icon'
import { codexTierToRateLimitInfo, formatRateLimitMessage } from '~/lib/rateLimitUtils'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { getCachedSettingsGroupLabel, getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { spinner } from '~/styles/animations.css'
import { markdownContent } from './markdownContent.css'
import {
  controlResponseMessage,
  resultDivider,
} from './messageStyles.css'
import { isObject } from './messageUtils'
import { formatDuration } from './rendererUtils'

// ---------------------------------------------------------------------------
// Display helpers for settings change notifications
// ---------------------------------------------------------------------------

function displayLabel(key: string): string {
  switch (key) {
    case 'model': return 'Model'
    case 'effort': return 'Effort'
    case 'permissionMode': return 'Permission Mode'
    default: return getCachedSettingsGroupLabel(key) ?? key
  }
}

function displayValue(key: string, value: string): string {
  return getCachedSettingsLabel(key, value) ?? value
}

/** Handles settings change notifications: {"type":"settings_changed","changes":{...}} */
export const settingsChangedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'settings_changed')
      return null
    const changes = parsed.changes as Record<string, { old: string, new: string, label?: string, oldLabel?: string, newLabel?: string }>
    if (!changes)
      return null
    const parts: string[] = []
    for (const [key, val] of Object.entries(changes)) {
      if (val.old !== val.new) {
        const oldDisplay = val.oldLabel || displayValue(key, val.old)
        const newDisplay = val.newLabel || displayValue(key, val.new)
        const label = val.label || displayLabel(key)
        if (oldDisplay)
          parts.push(`${label} (${oldDisplay} → ${newDisplay})`)
        else
          parts.push(`${label} (${newDisplay})`)
      }
    }
    if (parts.length === 0)
      return null
    return <div class={controlResponseMessage}>{parts.join(', ')}</div>
  },
}

/** Handles interrupt notifications: {"type":"interrupted"} */
export const interruptedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'interrupted')
      return null
    return <div class={controlResponseMessage}>Interrupted</div>
  },
}

export const compactingRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'compacting')
      return null
    return (
      <div class={resultDivider}>
        <Icon icon={LoaderCircle} size="sm" class={spinner} />
        {' Compacting context...'}
      </div>
    )
  },
}

export const contextClearedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'context_cleared')
      return null
    return <div class={controlResponseMessage}>Context cleared</div>
  },
}

/** Handles agent error notifications: {"type":"agent_error","error":"..."} */
export const agentErrorRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'agent_error')
      return null
    const error = typeof parsed.error === 'string' ? parsed.error : 'Unknown error'
    return <div class={controlResponseMessage}>{error}</div>
  },
}

/** Handles agent renamed notifications: {"type":"agent_renamed","title":"..."} */
export const agentRenamedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'agent_renamed')
      return null
    const title = typeof parsed.title === 'string' ? parsed.title : ''
    if (!title)
      return null
    return (
      <div class={controlResponseMessage}>
        {'Renamed to '}
        {title}
      </div>
    )
  },
}

function formatApiRetryLabel(data: Record<string, unknown>): string {
  const attempt = typeof data.attempt === 'number' ? data.attempt : '?'
  const maxRetries = typeof data.max_retries === 'number' ? data.max_retries : '?'
  const errorStatus = data.error_status != null ? String(data.error_status) : null
  const error = typeof data.error === 'string' ? data.error : null
  const detail = [errorStatus, error].filter(Boolean).join(' \u00B7 ')
  return detail
    ? `API Retry ${attempt}/${maxRetries} (${detail})`
    : `API Retry ${attempt}/${maxRetries}`
}

/** Handles api_retry notifications: {"type":"system","subtype":"api_retry","attempt":N,"max_retries":N,...} */
export const apiRetryRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'api_retry')
      return null
    return <div class={controlResponseMessage}>{formatApiRetryLabel(parsed as Record<string, unknown>)}</div>
  },
}

/** Handles rate limit notifications: {"type":"rate_limit","rate_limit_info":{...}} or Codex native format */
export const rateLimitRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed))
      return null
    // Existing Claude Code format: {type: "rate_limit", rate_limit_info: {...}}
    if (parsed.type === 'rate_limit') {
      const info = parsed.rate_limit_info
      if (!isObject(info))
        return <div class={controlResponseMessage}>Rate limit update</div>
      // Hide "allowed" status from chat — the popover still shows it.
      if ((info as Record<string, unknown>).status === 'allowed')
        return null
      return <div class={controlResponseMessage}>{formatRateLimitMessage(info as Record<string, unknown>)}</div>
    }
    // Codex native format: {method: "account/rateLimits/updated", params: {rateLimits: {...}}}
    if (parsed.method === 'account/rateLimits/updated')
      return renderCodexRateLimits(parsed)
    return null
  },
}

/** Render Codex native rate limit notification. */
function renderCodexRateLimits(parsed: Record<string, unknown>): JSXElement {
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
    parts.push(formatRateLimitMessage(info as unknown as Record<string, unknown>))
  }
  if (parts.length === 0)
    return null
  return <div class={controlResponseMessage}>{parts.join(', ')}</div>
}

// ---------------------------------------------------------------------------
// Context compaction boundary renderers
// ---------------------------------------------------------------------------

function formatTokenCount(n: number): string {
  if (n >= 1_000_000)
    return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000)
    return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

/** Handles compact_boundary messages: {"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"|"manual","pre_tokens":number}} */
export const compactBoundaryRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'compact_boundary')
      return null
    // Wire format uses snake_case compact_metadata; internal format uses camelCase compactMetadata.
    const meta = (isObject(parsed.compact_metadata) ? parsed.compact_metadata : parsed.compactMetadata) as Record<string, unknown> | undefined
    const trigger = meta?.trigger as string | undefined
    const preTokens = (typeof meta?.pre_tokens === 'number' ? meta.pre_tokens : meta?.preTokens) as number | undefined
    const parts = ['Context compacted']
    if (trigger)
      parts.push(`(${trigger})`)
    if (typeof preTokens === 'number')
      parts.push(`- was ${formatTokenCount(preTokens)} tokens`)
    return <div class={resultDivider}>{parts.join(' ')}</div>
  },
}

/** Handles microcompact_boundary messages: {"type":"system","subtype":"microcompact_boundary","microcompactMetadata":{"trigger":...,"preTokens":number,"tokensSaved":number,...}} */
export const microcompactBoundaryRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'microcompact_boundary')
      return null
    const meta = (isObject(parsed.microcompactMetadata) ? parsed.microcompactMetadata : parsed.microcompact_metadata) as Record<string, unknown> | undefined
    const trigger = meta?.trigger as string | undefined
    const tokensSaved = (typeof meta?.tokensSaved === 'number' ? meta.tokensSaved : meta?.tokens_saved) as number | undefined
    const parts = ['Context microcompacted']
    if (trigger)
      parts.push(`(${trigger})`)
    if (typeof tokensSaved === 'number')
      parts.push(`- saved ${formatTokenCount(tokensSaved)} tokens`)
    return <div class={resultDivider}>{parts.join(' ')}</div>
  },
}

/** Handles system init messages: {"type":"system","subtype":"init","session_id":"..."} — hidden at MessageBubble level */
export const systemInitRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'init')
      return null
    return <span />
  },
}

/** Handles result messages: {"type":"result","duration_ms":865,"num_turns":558,...} */
export const resultRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'result')
      return null

    // Error turn: show errors array if present (e.g. "No conversation found with session ID: ...")
    if (parsed.is_error === true) {
      const errors = Array.isArray(parsed.errors) ? parsed.errors as string[] : []
      const resultText = typeof parsed.result === 'string' ? parsed.result : ''
      const errorMsg = errors.length > 0 ? errors.join('; ') : resultText || 'Unknown error'
      return <div class={resultDivider} style={{ color: 'var(--danger)' }}>{errorMsg}</div>
    }

    const durationMs = typeof parsed.duration_ms === 'number' ? parsed.duration_ms : 0
    const resultText = typeof parsed.result === 'string' ? parsed.result : ''
    const durationStr = formatDuration(durationMs)

    // When stop_reason is absent (agent never produced output), show the result
    // text as an error — e.g. "Unknown skill: update-pr".
    // Local commands that produce real output (e.g. /context) complete with
    // num_turns > 1 despite having no stop_reason; skip the error path for those.
    const numTurns = typeof parsed.num_turns === 'number' ? parsed.num_turns : 0
    if (!parsed.stop_reason && numTurns <= 1 && resultText) {
      return <div class={resultDivider} style={{ color: 'var(--danger)' }}>{resultText}</div>
    }

    // For non-success subtypes, show result text with duration.
    // For success, the result repeats the last assistant message — skip it.
    const displayText = parsed.subtype !== 'success' ? resultText : ''
    const label = displayText
      ? `${displayText} (${durationStr})`
      : `Took ${durationStr}`
    return <div class={resultDivider}>{label}</div>
  },
}

/** Handles control response messages: {"isSynthetic":true,"controlResponse":{"action":"approved"|"rejected","comment":"..."}} */
export const controlResponseRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || !isObject(parsed.controlResponse))
      return null

    const cr = parsed.controlResponse as Record<string, unknown>
    const action = cr.action as string
    const comment = (cr.comment as string) || ''

    if (action === 'approved')
      return <div class={controlResponseMessage}>Approved</div>

    if (comment) {
      return (
        <div class={controlResponseMessage}>
          <div>
            <div>Sent feedback:</div>
            <div class={markdownContent} innerHTML={renderMarkdown(comment)} />
          </div>
        </div>
      )
    }

    return <div class={controlResponseMessage}>Rejected</div>
  },
}

// ---------------------------------------------------------------------------
// Aggregate notification thread renderer
// ---------------------------------------------------------------------------

/** Format compaction token info: "(167.3k ⤓ 50.2k)" or "(167.3k)" or "(⤓ 50.2k)" */
function formatCompactionTokens(preTokens: number | undefined, tokensSaved: number | undefined): string {
  const parts: string[] = []
  if (typeof preTokens === 'number')
    parts.push(formatTokenCount(preTokens))
  if (typeof tokensSaved === 'number')
    parts.push(`⤓ ${formatTokenCount(tokensSaved)}`)
  if (parts.length === 0)
    return ''
  return ` (${parts.join(' ')})`
}

/**
 * Renders a notification thread (multiple consolidated messages in a single wrapper)
 * as a combined notification. Used when Hub threads consecutive notifications together.
 */
export function renderNotificationThread(messages: unknown[]): JSXElement {
  // Accumulate state across all messages in the thread.
  const settingsParts: string[] = []
  let compacting = false
  let compactLabel: string | null = null
  let compactPreTokens: number | undefined
  let compactTokensSaved: number | undefined
  let microcompactLabel: string | null = null
  let microcompactPreTokens: number | undefined
  let microcompactTokensSaved: number | undefined
  const rateLimitByType: Record<string, Record<string, unknown>> = {}
  let latestApiRetry: Record<string, unknown> | null = null

  for (const msg of messages) {
    if (!isObject(msg))
      continue
    const m = msg as Record<string, unknown>
    const t = m.type as string | undefined
    const st = m.subtype as string | undefined

    if (m.method === 'account/rateLimits/updated') {
      const params = m.params as Record<string, unknown> | undefined
      const rl = params?.rateLimits as Record<string, unknown> | undefined
      for (const tierKey of ['primary', 'secondary']) {
        const tier = rl?.[tierKey] as Record<string, unknown> | undefined
        if (!tier)
          continue
        const info = codexTierToRateLimitInfo(tier)
        if (info.rateLimitType)
          rateLimitByType[info.rateLimitType] = { ...info } as unknown as Record<string, unknown>
      }
    }
    else if (t === 'rate_limit') {
      const info = m.rate_limit_info
      if (isObject(info)) {
        const rlInfo = info as Record<string, unknown>
        const key = (typeof rlInfo.rateLimitType === 'string' && rlInfo.rateLimitType) || 'unknown'
        rateLimitByType[key] = rlInfo
      }
    }
    else if (t === 'settings_changed') {
      const changes = m.changes as Record<string, { old: string, new: string }> | undefined
      if (changes) {
        for (const [key, val] of Object.entries(changes)) {
          if (val.old !== val.new)
            settingsParts.push(`${displayLabel(key)} (${displayValue(key, val.old)} → ${displayValue(key, val.new)})`)
        }
      }
    }
    else if (t === 'context_cleared') {
      settingsParts.push('Context cleared')
    }
    else if (t === 'plan_execution') {
      settingsParts.push('Executing plan')
    }
    else if (t === 'agent_error') {
      const error = typeof m.error === 'string' ? m.error : 'Unknown error'
      settingsParts.push(error)
    }
    else if (t === 'interrupted') {
      settingsParts.push('Interrupted')
    }
    else if (t === 'agent_renamed') {
      const title = typeof m.title === 'string' ? m.title : ''
      if (title)
        settingsParts.push(`Renamed to ${title}`)
    }
    else if (t === 'system' && st === 'api_retry') {
      latestApiRetry = m
    }
    else if (t === 'compacting' || (t === 'system' && st === 'status' && m.status === 'compacting')) {
      compacting = true
    }
    else if (t === 'system' && st === 'status') {
      compacting = false
    }
    else if (t === 'system' && st === 'compact_boundary') {
      compacting = false
      const meta = (isObject(m.compact_metadata) ? m.compact_metadata : m.compactMetadata) as Record<string, unknown> | undefined
      compactPreTokens = (typeof meta?.pre_tokens === 'number' ? meta.pre_tokens : meta?.preTokens) as number | undefined
      compactTokensSaved = (typeof meta?.tokens_saved === 'number' ? meta.tokens_saved : meta?.tokensSaved) as number | undefined
      compactLabel = `Context compacted${formatCompactionTokens(compactPreTokens, compactTokensSaved)}`
    }
    else if (t === 'system' && st === 'microcompact_boundary') {
      compacting = false
      const meta = (isObject(m.microcompactMetadata) ? m.microcompactMetadata : m.microcompact_metadata) as Record<string, unknown> | undefined
      microcompactPreTokens = (typeof meta?.preTokens === 'number' ? meta.preTokens : meta?.pre_tokens) as number | undefined
      microcompactTokensSaved = (typeof meta?.tokensSaved === 'number' ? meta.tokensSaved : meta?.tokens_saved) as number | undefined
      microcompactLabel = `Context microcompacted${formatCompactionTokens(microcompactPreTokens, microcompactTokensSaved)}`
    }
  }

  // Add rate limit messages (one per rateLimitType), skipping "allowed" status.
  for (const info of Object.values(rateLimitByType)) {
    if (info.status !== 'allowed')
      settingsParts.push(formatRateLimitMessage(info))
  }

  const settingsLine = settingsParts.length > 0 ? settingsParts.join(', ') : null

  const elements: JSXElement[] = []

  // Compaction in progress (spinner).
  if (compacting && !compactLabel && !microcompactLabel) {
    elements.push(
      <div class={resultDivider}>
        <Icon icon={LoaderCircle} size="sm" class={spinner} />
        {' Compacting context...'}
      </div>,
    )
  }

  // Compact boundary result.
  if (compactLabel) {
    elements.push(
      <div class={resultDivider}>
        <Icon icon={ArrowDownToLine} size="sm" />
        {` ${compactLabel}`}
      </div>,
    )
  }

  // Microcompact boundary result.
  if (microcompactLabel) {
    elements.push(
      <div class={resultDivider}>
        <Icon icon={ArrowDownToLine} size="sm" />
        {` ${microcompactLabel}`}
      </div>,
    )
  }

  // API retry (latest only).
  if (latestApiRetry) {
    elements.push(
      <div class={controlResponseMessage}>{formatApiRetryLabel(latestApiRetry)}</div>,
    )
  }

  // Settings / context cleared / interrupted line.
  if (settingsLine) {
    elements.push(
      <div class={controlResponseMessage}>{settingsLine}</div>,
    )
  }

  if (elements.length === 0)
    return null

  if (elements.length === 1)
    return elements[0]

  return <div>{elements}</div>
}

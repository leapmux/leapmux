/* eslint-disable solid/components-return-once -- render methods are not Solid components */
/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSXElement } from 'solid-js'
import type { MessageContentRenderer } from './messageRenderers'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { formatRateLimitMessage } from '~/lib/rateLimitUtils'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { spinner } from '~/styles/animations.css'
import { EFFORT_LABELS, MODEL_LABELS, PERMISSION_MODE_LABELS } from '~/utils/controlResponse'
import { markdownContent } from './markdownContent.css'
import { isObject } from './messageRenderers'
import {
  controlResponseMessage,
  resultDivider,
} from './messageStyles.css'

// ---------------------------------------------------------------------------
// Display helpers for settings change notifications
// ---------------------------------------------------------------------------

function displayLabel(key: string): string {
  switch (key) {
    case 'model': return 'Model'
    case 'effort': return 'Effort'
    case 'permissionMode': return 'Mode'
    default: return key
  }
}

const DISPLAY_VALUE_MAPS: Record<string, Record<string, string>> = {
  model: MODEL_LABELS,
  effort: EFFORT_LABELS,
  permissionMode: PERMISSION_MODE_LABELS,
}

function displayValue(key: string, value: string): string {
  return DISPLAY_VALUE_MAPS[key]?.[value] ?? value
}

/** Handles settings change notifications: {"type":"settings_changed","changes":{...},"contextCleared"?:true} */
export const settingsChangedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'settings_changed')
      return null
    const changes = parsed.changes as Record<string, { old: string, new: string }>
    if (!changes)
      return null
    const parts: string[] = []
    for (const [key, val] of Object.entries(changes)) {
      if (val.old !== val.new) {
        parts.push(`${displayLabel(key)} (${displayValue(key, val.old)} → ${displayValue(key, val.new)})`)
      }
    }
    if (parsed.contextCleared) {
      parts.push('Context cleared')
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

export const contextClearedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'context_cleared')
      return null
    return <div class={controlResponseMessage}>Context cleared</div>
  },
}

/** Handles rate limit notifications: {"type":"rate_limit","rate_limit_info":{...}} */
export const rateLimitRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'rate_limit')
      return null
    const info = parsed.rate_limit_info
    if (!isObject(info))
      return <div class={controlResponseMessage}>Rate limit update</div>
    // Hide "allowed" status from chat — the popover still shows it.
    if ((info as Record<string, unknown>).status === 'allowed')
      return null
    return <div class={controlResponseMessage}>{formatRateLimitMessage(info as Record<string, unknown>)}</div>
  },
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

export function formatDuration(ms: number): string {
  if (ms < 1000)
    return `${Math.round(ms)}ms`

  const totalSeconds = ms / 1000
  if (totalSeconds < 10)
    return `${totalSeconds.toFixed(1)}s`

  const totalSecondsRounded = Math.round(totalSeconds)
  const days = Math.floor(totalSecondsRounded / 86400)
  const hours = Math.floor((totalSecondsRounded % 86400) / 3600)
  const minutes = Math.floor((totalSecondsRounded % 3600) / 60)
  const seconds = totalSecondsRounded % 60

  const parts: string[] = []
  if (days > 0)
    parts.push(`${days}d`)
  if (hours > 0)
    parts.push(`${hours}h`)
  if (minutes > 0)
    parts.push(`${minutes}m`)
  if (seconds > 0 || parts.length === 0)
    parts.push(`${seconds}s`)
  return parts.join(' ')
}

/** Handles result messages: {"type":"result","duration_ms":865,"num_turns":558,...} */
export const resultRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'result')
      return null
    const durationMs = typeof parsed.duration_ms === 'number' ? parsed.duration_ms : 0
    // Only show result text for non-success outcomes (e.g. "Unknown skill: clear").
    // Successful turns repeat the last assistant message, which is already visible.
    const resultText = parsed.subtype !== 'success' && typeof parsed.result === 'string' ? parsed.result : ''
    const durationStr = formatDuration(durationMs)
    const label = resultText
      ? `${resultText} (${durationStr})`
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
            <div>Rejected:</div>
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
  let contextCleared = false
  let planExecContextCleared = false
  let interrupted = false
  let compacting = false
  let compactLabel: string | null = null
  let compactPreTokens: number | undefined
  let compactTokensSaved: number | undefined
  let microcompactLabel: string | null = null
  let microcompactPreTokens: number | undefined
  let microcompactTokensSaved: number | undefined
  const rateLimitByType: Record<string, Record<string, unknown>> = {}
  // Track whether the most recent context-affecting event is compaction or context_cleared.
  // true = compaction came last, false = context_cleared came last.
  let lastContextEventIsCompaction = false

  for (const msg of messages) {
    if (!isObject(msg))
      continue
    const m = msg as Record<string, unknown>
    const t = m.type as string | undefined
    const st = m.subtype as string | undefined

    if (t === 'rate_limit') {
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
      if (m.contextCleared) {
        contextCleared = true
        lastContextEventIsCompaction = false
      }
    }
    else if (t === 'context_cleared') {
      contextCleared = true
      lastContextEventIsCompaction = false
    }
    else if (t === 'plan_execution') {
      if (m.context_cleared === true) {
        planExecContextCleared = true
        settingsParts.push('Executing plan with clean context')
      }
      else {
        settingsParts.push('Executing plan retaining context')
      }
    }
    else if (t === 'interrupted') {
      interrupted = true
    }
    else if (t === 'system' && st === 'status') {
      compacting = m.status === 'compacting'
      if (compacting)
        lastContextEventIsCompaction = true
    }
    else if (t === 'system' && st === 'compact_boundary') {
      compacting = false
      lastContextEventIsCompaction = true
      const meta = (isObject(m.compact_metadata) ? m.compact_metadata : m.compactMetadata) as Record<string, unknown> | undefined
      compactPreTokens = (typeof meta?.pre_tokens === 'number' ? meta.pre_tokens : meta?.preTokens) as number | undefined
      compactTokensSaved = (typeof meta?.tokens_saved === 'number' ? meta.tokens_saved : meta?.tokensSaved) as number | undefined
      compactLabel = `Context compacted${formatCompactionTokens(compactPreTokens, compactTokensSaved)}`
    }
    else if (t === 'system' && st === 'microcompact_boundary') {
      compacting = false
      lastContextEventIsCompaction = true
      const meta = (isObject(m.microcompactMetadata) ? m.microcompactMetadata : m.microcompact_metadata) as Record<string, unknown> | undefined
      microcompactPreTokens = (typeof meta?.preTokens === 'number' ? meta.preTokens : meta?.pre_tokens) as number | undefined
      microcompactTokensSaved = (typeof meta?.tokensSaved === 'number' ? meta.tokensSaved : meta?.tokens_saved) as number | undefined
      microcompactLabel = `Context microcompacted${formatCompactionTokens(microcompactPreTokens, microcompactTokensSaved)}`
    }
  }

  if (contextCleared && !planExecContextCleared && !lastContextEventIsCompaction)
    settingsParts.push('Context cleared')
  if (interrupted)
    settingsParts.push('Interrupted')

  // Add rate limit messages (one per rateLimitType), skipping "allowed" status.
  for (const info of Object.values(rateLimitByType)) {
    if (info.status !== 'allowed')
      settingsParts.push(formatRateLimitMessage(info))
  }

  const settingsLine = settingsParts.length > 0 ? settingsParts.join(', ') : null

  // Show compaction when there's no context_cleared, or when compaction came after context_cleared.
  const showCompaction = !contextCleared || lastContextEventIsCompaction

  const elements: JSXElement[] = []

  // Compaction in progress (spinner).
  if (showCompaction && compacting && !compactLabel && !microcompactLabel) {
    elements.push(
      <div class={resultDivider}>
        <LoaderCircle size={14} class={spinner} />
        {' Compacting context...'}
      </div>,
    )
  }

  // Compact boundary result.
  if (showCompaction && compactLabel) {
    elements.push(
      <div class={resultDivider}>
        <ArrowDownToLine size={14} />
        {` ${compactLabel}`}
      </div>,
    )
  }

  // Microcompact boundary result.
  if (showCompaction && microcompactLabel) {
    elements.push(
      <div class={resultDivider}>
        <ArrowDownToLine size={14} />
        {` ${microcompactLabel}`}
      </div>,
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

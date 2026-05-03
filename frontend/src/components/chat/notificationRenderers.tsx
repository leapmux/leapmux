/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSXElement } from 'solid-js'
import type { MessageContentRenderer } from './messageRenderers'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Icon } from '~/components/common/Icon'
import { isObject, pickFirstNumber, pickFirstObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { NOTIFICATION_TYPE } from '~/lib/notificationTypes'
import { getCachedSettingsGroupLabel, getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { spinner } from '~/styles/animations.css'
import { MarkdownText } from './messageRenderers'
import {
  controlResponseMessage,
  resultDivider,
} from './messageStyles.css'
import { providerFor } from './providers/registry'
import { formatTokenCount } from './rendererUtils'
import { PERMISSION_MODE_KEY } from './settingsShared'

// ---------------------------------------------------------------------------
// Display helpers for settings change notifications
// ---------------------------------------------------------------------------

function displayLabel(key: string): string {
  switch (key) {
    case 'model': return 'Model'
    case 'effort': return 'Effort'
    case PERMISSION_MODE_KEY: return 'Permission Mode'
    default: return getCachedSettingsGroupLabel(key) ?? key
  }
}

function displayValue(key: string, value: string): string {
  return getCachedSettingsLabel(key, value) ?? value
}

/** Handles settings change notifications: {"type":"settings_changed","changes":{...}} */
export const settingsChangedRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.SettingsChanged)
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
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.Interrupted)
      return null
    return <div class={controlResponseMessage}>Interrupted</div>
  },
}

/**
 * Handles compacting status notifications. The canonical shape is the raw
 * Claude `system` message: `{type:"system",subtype:"status",status:"compacting"}`.
 */
export const compactingRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed))
      return null
    if (!(parsed.type === 'system' && parsed.subtype === 'status' && parsed.status === NOTIFICATION_TYPE.Compacting))
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
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.ContextCleared)
      return null
    return <div class={controlResponseMessage}>Context cleared</div>
  },
}

/** Handles agent error notifications: {"type":"agent_error","error":"..."} */
export const agentErrorRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.AgentError)
      return null
    const error = pickString(parsed, 'error', 'Unknown error')
    return <div class={controlResponseMessage}>{error}</div>
  },
}

/**
 * Handles plan_updated notifications:
 * `{"type":"plan_updated","plan_title":"...","plan_file_path":"...","update_agent_title"?:true}`.
 *
 * Two display variants:
 * - With `update_agent_title:true`: "Plan updated and renamed to <title>".
 * - Without:                       "Plan updated: <title>".
 */
export const planUpdatedRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.PlanUpdated)
      return null
    const title = pickString(parsed, 'plan_title')
    if (!title)
      return null
    if (parsed.update_agent_title === true) {
      return (
        <div class={controlResponseMessage}>
          {'Plan updated and renamed to '}
          {title}
        </div>
      )
    }
    return (
      <div class={controlResponseMessage}>
        {'Plan updated: '}
        {title}
      </div>
    )
  },
}

function formatApiRetryLabel(data: Record<string, unknown>): string {
  const attempt = pickNumber(data, 'attempt', '?' as const)
  const maxRetries = pickNumber(data, 'max_retries', '?' as const)
  const errorStatus = data.error_status != null ? String(data.error_status) : null
  const error = pickString(data, 'error', null)
  const detail = [errorStatus, error].filter(Boolean).join(' ')
  return detail
    ? `API Retry ${attempt}/${maxRetries} (${detail})`
    : `API Retry ${attempt}/${maxRetries}`
}

/** Handles api_retry notifications: {"type":"system","subtype":"api_retry","attempt":N,"max_retries":N,...} */
export const apiRetryRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'api_retry')
      return null
    return <div class={controlResponseMessage}>{formatApiRetryLabel(parsed as Record<string, unknown>)}</div>
  },
}

// ---------------------------------------------------------------------------
// Context compaction boundary renderers
// ---------------------------------------------------------------------------

/**
 * Handles compact_boundary messages. Recognizes two shapes:
 *  - Claude raw `system` message: `{type:"system",subtype:"compact_boundary",compact_metadata:{trigger,pre_tokens}}`
 *  - Codex raw JSON-RPC notification: `{method:"thread/compacted",params:{threadId,turnId}}`
 */
export const compactBoundaryRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed))
      return null
    const isClaude = parsed.type === 'system' && parsed.subtype === 'compact_boundary'
    const isCodex = parsed.method === 'thread/compacted'
    if (!isClaude && !isCodex)
      return null
    // Claude carries metadata under `compact_metadata` / `compactMetadata`.
    // Codex carries no metadata today; the message itself is the boundary.
    const meta = pickFirstObject(parsed, ['compact_metadata', 'compactMetadata'])
    const trigger = meta?.trigger as string | undefined
    const preTokens = pickFirstNumber(meta, ['pre_tokens', 'preTokens'])
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
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'microcompact_boundary')
      return null
    const meta = pickFirstObject(parsed, ['microcompactMetadata', 'microcompact_metadata'])
    const trigger = meta?.trigger as string | undefined
    const tokensSaved = pickFirstNumber(meta, ['tokensSaved', 'tokens_saved'])
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
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'init')
      return null
    return <span />
  },
}

/** Handles control response messages: {"isSynthetic":true,"controlResponse":{"action":"approved"|"rejected","comment":"..."}} */
export const controlResponseRenderer: MessageContentRenderer = {
  render(parsed, _context) {
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
            <MarkdownText text={comment} />
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
 * Walk a single message in a notification_thread wrapper, producing one or
 * more flat thread entries via the provider plugin (if any) followed by the
 * provider-neutral switch. Returns an array — the caller appends.
 */
function threadEntriesFor(
  m: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
): Array<{ kind: 'text', text: string } | { kind: 'group', groupKey: string, prefix: string, entry: string } | { kind: 'divider', text: string, loading?: boolean }> {
  const plugin = agentProvider != null ? providerFor(agentProvider) : undefined
  const fromPlugin = plugin?.notificationThreadEntry?.(m)
  if (fromPlugin !== null && fromPlugin !== undefined)
    return fromPlugin

  const t = m.type as string | undefined
  const st = m.subtype as string | undefined

  if (t === NOTIFICATION_TYPE.SettingsChanged) {
    const changes = m.changes as Record<string, { old: string, new: string }> | undefined
    if (!changes)
      return []
    const parts: string[] = []
    for (const [key, val] of Object.entries(changes)) {
      if (val.old !== val.new)
        parts.push(`${displayLabel(key)} (${displayValue(key, val.old)} → ${displayValue(key, val.new)})`)
    }
    return parts.length > 0 ? [{ kind: 'text', text: parts.join(', ') }] : []
  }
  if (t === NOTIFICATION_TYPE.ContextCleared)
    return [{ kind: 'text', text: 'Context cleared' }]
  if (t === NOTIFICATION_TYPE.PlanExecution)
    return [{ kind: 'text', text: 'Executing plan' }]
  if (t === NOTIFICATION_TYPE.AgentError)
    return [{ kind: 'text', text: pickString(m, 'error', 'Unknown error') }]
  if (t === NOTIFICATION_TYPE.Interrupted)
    return [{ kind: 'text', text: 'Interrupted' }]
  if (t === NOTIFICATION_TYPE.PlanUpdated) {
    const title = pickString(m, 'plan_title')
    if (!title)
      return []
    if (m.update_agent_title === true)
      return [{ kind: 'text', text: `Plan updated and renamed to ${title}` }]
    return [{ kind: 'text', text: `Plan updated: ${title}` }]
  }
  if (t === 'system' && st === 'api_retry')
    return [{ kind: 'text', text: formatApiRetryLabel(m) }]
  if (t === 'system' && st === 'status' && m.status === NOTIFICATION_TYPE.Compacting)
    return [{ kind: 'divider', text: 'Compacting context...', loading: true }]
  if (t === 'system' && st === 'compact_boundary') {
    const meta = pickFirstObject(m, ['compact_metadata', 'compactMetadata'])
    const pre = pickFirstNumber(meta, ['pre_tokens', 'preTokens'])
    const saved = pickFirstNumber(meta, ['tokens_saved', 'tokensSaved'])
    return [{ kind: 'divider', text: `Context compacted${formatCompactionTokens(pre, saved)}` }]
  }
  // Codex `thread/compacted` is the boundary signal; no metadata fields.
  if (m.method === 'thread/compacted')
    return [{ kind: 'divider', text: 'Context compacted' }]
  // Codex `item/started` of a contextCompaction item is the in-progress
  // spinner. The completed boundary arrives later via `thread/compacted`.
  if (m.method === 'item/started') {
    const params = pickObject(m, 'params')
    const item = pickObject(params, 'item')
    if (item && item.type === 'contextCompaction')
      return [{ kind: 'divider', text: 'Compacting context...', loading: true }]
  }
  if (t === 'system' && st === 'microcompact_boundary') {
    const meta = pickFirstObject(m, ['microcompactMetadata', 'microcompact_metadata'])
    const pre = pickFirstNumber(meta, ['preTokens', 'pre_tokens'])
    const saved = pickFirstNumber(meta, ['tokensSaved', 'tokens_saved'])
    return [{ kind: 'divider', text: `Context microcompacted${formatCompactionTokens(pre, saved)}` }]
  }
  return []
}

/**
 * Renders a notification thread (multiple consolidated messages in a single wrapper)
 * as a combined notification. Used when Hub threads consecutive notifications together.
 *
 * `agentProvider` is consulted via `plugin.notificationThreadEntry` for any
 * provider-specific messages (e.g. Codex MCP startup statuses) before the
 * shared switch handles provider-neutral types.
 */
export function renderNotificationThread(messages: unknown[], agentProvider?: AgentProvider): JSXElement {
  type RenderEntry = { kind: 'text', text: string } | { kind: 'divider', text: string, loading?: boolean }
  const entries: RenderEntry[] = []
  const groupOrder: string[] = []
  const groups = new Map<string, { prefix: string, entries: string[] }>()

  const flushGroups = () => {
    if (groupOrder.length === 0)
      return
    for (const key of groupOrder) {
      const group = groups.get(key)
      if (!group || group.entries.length === 0)
        continue
      entries.push({ kind: 'text', text: `${group.prefix}: ${group.entries.join(', ')}` })
    }
    groups.clear()
    groupOrder.length = 0
  }

  for (const msg of messages) {
    if (!isObject(msg))
      continue
    const produced = threadEntriesFor(msg as Record<string, unknown>, agentProvider)
    for (const entry of produced) {
      if (entry.kind === 'group') {
        const existing = groups.get(entry.groupKey)
        if (existing) {
          existing.entries.push(entry.entry)
        }
        else {
          groups.set(entry.groupKey, { prefix: entry.prefix, entries: [entry.entry] })
          groupOrder.push(entry.groupKey)
        }
        continue
      }
      flushGroups()
      entries.push(entry)
    }
  }

  flushGroups()

  const elements: JSXElement[] = []
  let pendingText: string[] = []

  const flushPendingText = () => {
    if (pendingText.length === 0)
      return
    elements.push(
      <div class={controlResponseMessage}>{pendingText.join(', ')}</div>,
    )
    pendingText = []
  }

  for (const entry of entries) {
    if (entry.kind === 'text') {
      pendingText.push(entry.text)
      continue
    }

    flushPendingText()
    elements.push(
      <div class={resultDivider}>
        <Icon icon={entry.loading ? LoaderCircle : ArrowDownToLine} size="sm" class={entry.loading ? spinner : undefined} />
        {` ${entry.text}`}
      </div>,
    )
  }
  flushPendingText()

  if (elements.length === 0)
    return null

  if (elements.length === 1)
    return elements[0]

  return <div>{elements}</div>
}

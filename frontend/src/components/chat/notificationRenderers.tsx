/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSXElement } from 'solid-js'
import type { MessageContentRenderer } from './messageRenderers'
import type { NotificationThreadEntry } from './providers/registry'
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

// Provider-neutral notification labels referenced by BOTH the standalone
// per-message renderers and the notification-thread switch. Hoisted to shared
// constants so the wording cannot drift between the two paths -- the same
// anti-drift guarantee COMPACTING_LABEL / MICROCOMPACT_LABEL give the compaction
// rows below.
const CONTEXT_CLEARED_LABEL = 'Context cleared'
const INTERRUPTED_LABEL = 'Interrupted'
const UNKNOWN_ERROR_LABEL = 'Unknown error'

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

/**
 * A single settings change after display resolution, ready to format. Parsing
 * (which reads the raw, untyped wire fields) is separated from formatting (which
 * assembles the label) so the defensive field reads live in one place.
 */
interface SettingsChange {
  label: string
  /** No prior value -- render the "(new)"-only form instead of an old -> new transition. */
  firstSet: boolean
  oldDisplay: string
  newDisplay: string
}

/**
 * Parse the raw, untyped `changes` map into resolved {@link SettingsChange}s.
 * Each entry is expected to be `{ old, new, label?, oldLabel?, newLabel? }`:
 * inline `label`/`oldLabel`/`newLabel` overrides win over the cache-derived
 * display, unchanged entries (old === new) are dropped, and non-object entries
 * are skipped so a malformed payload degrades rather than throws.
 */
function parseSettingsChanges(changes: unknown): SettingsChange[] {
  if (!isObject(changes))
    return []
  const result: SettingsChange[] = []
  for (const [key, val] of Object.entries(changes)) {
    if (!isObject(val))
      continue
    const oldValue = pickString(val, 'old')
    const newValue = pickString(val, 'new')
    if (oldValue === newValue)
      continue
    result.push({
      // `?? ` (not `||`) so an explicit empty-string override is honored rather
      // than silently falling back to the cache-derived display.
      label: pickString(val, 'label', undefined) ?? displayLabel(key),
      // Gate the "(new)"-only form on the absence of an old VALUE, not on an
      // empty old DISPLAY: a real value whose display resolves to "" is still a
      // transition and must keep the arrow.
      firstSet: oldValue === '',
      oldDisplay: pickString(val, 'oldLabel', undefined) ?? displayValue(key, oldValue),
      newDisplay: pickString(val, 'newLabel', undefined) ?? displayValue(key, newValue),
    })
  }
  return result
}

/**
 * Format settings changes as `Label (old → new)` parts, degrading to
 * `Label (new)` when there is no old value. Shared by the standalone renderer
 * and the notification-thread switch so both render settings changes identically.
 */
function formatSettingsChanges(changes: unknown): string[] {
  return parseSettingsChanges(changes).map(c =>
    c.firstSet ? `${c.label} (${c.newDisplay})` : `${c.label} (${c.oldDisplay} → ${c.newDisplay})`,
  )
}

/** Handles settings change notifications: {"type":"settings_changed","changes":{...}} */
export const settingsChangedRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.SettingsChanged)
      return null
    const parts = formatSettingsChanges(parsed.changes)
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
    return <div class={controlResponseMessage}>{INTERRUPTED_LABEL}</div>
  },
}

export const contextClearedRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.ContextCleared)
      return null
    return <div class={controlResponseMessage}>{CONTEXT_CLEARED_LABEL}</div>
  },
}

/** Handles agent error notifications: {"type":"agent_error","error":"..."} */
export const agentErrorRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.AgentError)
      return null
    const error = pickString(parsed, 'error', UNKNOWN_ERROR_LABEL)
    return <div class={controlResponseMessage}>{error}</div>
  },
}

/**
 * Build the plan_updated label, or null when there is no title to show. Two
 * variants: "Plan updated and renamed to <title>" when `update_agent_title` is
 * set, else "Plan updated: <title>". Shared by the standalone renderer and the
 * notification-thread switch so the two cannot drift on the wording.
 */
function planUpdatedLabel(source: Record<string, unknown>): string | null {
  const title = pickString(source, 'plan_title')
  if (!title)
    return null
  return source.update_agent_title === true
    ? `Plan updated and renamed to ${title}`
    : `Plan updated: ${title}`
}

/**
 * Handles plan_updated notifications:
 * `{"type":"plan_updated","plan_title":"...","plan_file_path":"...","update_agent_title"?:true}`.
 */
export const planUpdatedRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== NOTIFICATION_TYPE.PlanUpdated)
      return null
    const label = planUpdatedLabel(parsed)
    if (label === null)
      return null
    return <div class={controlResponseMessage}>{label}</div>
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
    return <div class={controlResponseMessage}>{formatApiRetryLabel(parsed)}</div>
  },
}

// ---------------------------------------------------------------------------
// Context compaction boundary renderers
// ---------------------------------------------------------------------------

/**
 * A compaction divider row: a leading icon (the down-to-line arrow, or a
 * spinner while compaction is in progress) followed by the label. Shared by the
 * standalone boundary renderers and the aggregate notification thread so a
 * boundary renders identically whether shown on its own or consolidated.
 * Exported so a provider that surfaces its own compaction event (e.g. Pi) can
 * render the same icon + layout as Claude/Codex, not just the same label text.
 */
export function CompactionDivider(props: { text: string, loading?: boolean }): JSXElement {
  return (
    <div class={resultDivider}>
      <Icon icon={props.loading ? LoaderCircle : ArrowDownToLine} size="sm" class={props.loading ? spinner : undefined} />
      {` ${props.text}`}
    </div>
  )
}

// Boundary labels with no per-message detail. Each is referenced by both the
// standalone renderer and the notification-thread switch so the wording cannot
// drift between the two paths (the same guarantee compactBoundaryLabel gives
// the compacted boundary). The microcompact label has no detail because Claude
// Code emits no microcompact metadata object.
export const COMPACTING_LABEL = 'Compacting context...'
const MICROCOMPACT_LABEL = 'Context microcompacted'

// compact_boundary carries its metadata under a snake_case key in the SDK
// stream-json output (compact_metadata) or a camelCase key in the .jsonl
// transcript form (compactMetadata); resolve against both from one definition
// so the standalone and aggregate render paths can never drift on the key list.
// The order is immaterial -- a message carries one casing, and pickFirstObject
// returns the first present.
//
// microcompact_boundary has no analogous list: Claude Code emits no microcompact
// metadata object, so that boundary renders a plain label with no detail.
const COMPACT_META_KEYS = ['compact_metadata', 'compactMetadata'] as const

/**
 * Normalized, provider-agnostic compaction detail. Each render path parses its
 * own wire shape into this once (see {@link parseCompactionMeta}); a provider
 * that surfaces its own compaction event (e.g. Pi) constructs it directly. The
 * formatters below consume only this typed shape, so they never touch raw
 * snake/camelCase wire keys and a provider can't accidentally couple to them.
 */
export interface CompactionDetail {
  /** Trigger word shown first in the parenthetical, e.g. "manual"/"auto". */
  trigger?: string
  /** Pre-compaction context size, in tokens. */
  pre?: number
  /** Post-compaction context size, in tokens. */
  post?: number
}

/**
 * Coerce a raw numeric token count to a displayable value: finite and
 * non-negative. Non-finite inputs (NaN/Infinity -- which JSON can't carry but a
 * synthesized payload could) degrade to undefined so the detail is omitted
 * rather than rendering "NaN"; negatives clamp to 0, so a provider reporting an
 * explicit negative count (or a derived `pre - saved` where saved > pre) shows 0
 * instead of a negative size.
 */
function toTokenCount(n: number | undefined): number | undefined {
  if (n === undefined || !Number.isFinite(n))
    return undefined
  return Math.max(0, n)
}

/**
 * Resolve the post-compaction token count from raw metadata. `post_tokens`
 * (Claude's `compact_boundary` carries it directly) wins; as a fallback, when
 * only a `tokens_saved` delta is present alongside `pre`, post is derived as
 * `pre - saved` (which {@link toTokenCount} later clamps to >= 0). Returns
 * undefined when post cannot be resolved. Field names appear in both snake_case
 * (SDK stream) and camelCase (transcript) forms.
 */
function resolvePostTokens(meta: Record<string, unknown> | undefined, pre: number | undefined): number | undefined {
  const post = pickFirstNumber(meta, ['post_tokens', 'postTokens'])
  if (typeof post === 'number')
    return post
  const saved = pickFirstNumber(meta, ['tokens_saved', 'tokensSaved'])
  if (typeof pre === 'number' && typeof saved === 'number')
    return pre - saved
  return undefined
}

/**
 * Parse a raw compaction-metadata object (snake_case SDK stream or camelCase
 * transcript keys) into the provider-neutral {@link CompactionDetail}. Token
 * sanitization is deferred to {@link formatCompactionDetail} so every caller --
 * wire-derived or provider-synthesized -- gets the same clamping.
 */
function parseCompactionMeta(meta: Record<string, unknown> | undefined): CompactionDetail {
  const pre = pickFirstNumber(meta, ['pre_tokens', 'preTokens'])
  return {
    trigger: pickString(meta, 'trigger', undefined),
    pre,
    post: resolvePostTokens(meta, pre),
  }
}

/**
 * Format a pre/post token pair as the transition "105.4k → 8.5k", degrading to
 * "105.4k" (pre only), "→ 8.5k" (post only), or "" when neither is known.
 */
function formatTokenTransition(pre: number | undefined, post: number | undefined): string {
  if (typeof pre === 'number' && typeof post === 'number')
    return `${formatTokenCount(pre)} → ${formatTokenCount(post)}`
  if (typeof pre === 'number')
    return formatTokenCount(pre)
  if (typeof post === 'number')
    return `→ ${formatTokenCount(post)}`
  return ''
}

/**
 * Format a {@link CompactionDetail} as the parenthetical detail
 * " (manual, 105.4k → 8.5k)". The parenthetical holds an optional `trigger`
 * (e.g. "manual"/"auto") followed by the token transition (see
 * {@link formatTokenTransition}); token counts are sanitized via
 * {@link toTokenCount} first, for every provider. Each part is optional, so the
 * result can be " (manual)", " (manual, 105.4k)", " (105.4k → 8.5k)",
 * " (→ 8.5k)", or "" when nothing is known.
 */
function formatCompactionDetail(detail: CompactionDetail): string {
  const tokens = formatTokenTransition(toTokenCount(detail.pre), toTokenCount(detail.post))
  const parts: string[] = []
  if (detail.trigger)
    parts.push(detail.trigger)
  if (tokens)
    parts.push(tokens)
  return parts.length > 0 ? ` (${parts.join(', ')})` : ''
}

/**
 * "Context compacted" plus the formatted token detail. Exported so a provider
 * that surfaces its own compaction event (e.g. Pi, whose `compaction_end`
 * carries a reason + pre-compaction size) can render the identical label by
 * passing a {@link CompactionDetail} directly -- without re-implementing the
 * prefix or the token format, and without impersonating the Claude wire shape.
 */
export function compactedLabel(detail: CompactionDetail): string {
  return `Context compacted${formatCompactionDetail(detail)}`
}

/**
 * Build the compact_boundary label from a raw message: resolve the metadata
 * object (snake/camel), parse it into a {@link CompactionDetail}, then format via
 * {@link compactedLabel}. Shared by the standalone renderer and the
 * notification-thread switch so the label and the metadata-key list can never
 * drift between the two paths.
 */
function compactBoundaryLabel(source: Record<string, unknown>): string {
  return compactedLabel(parseCompactionMeta(pickFirstObject(source, COMPACT_META_KEYS)))
}

// Recognition predicates for the compaction message shapes. Each is referenced
// by BOTH the standalone renderer and the notification-thread switch so the two
// paths agree on WHICH message is a boundary -- not merely on the label text.
// Without them each path hand-repeats the type/subtype/method checks and could
// drift (the same anti-drift guarantee compactBoundaryLabel gives the label).

/** Claude's in-progress compacting status: `{type:system,subtype:status,status:compacting}`. */
function isCompactingStatus(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'status' && m.status === NOTIFICATION_TYPE.Compacting
}

/**
 * A completed compaction boundary: Claude's `compact_boundary` system message or
 * Codex's `thread/compacted` JSON-RPC notification -- both the same signal.
 */
function isCompactBoundary(m: Record<string, unknown>): boolean {
  return (m.type === 'system' && m.subtype === 'compact_boundary') || m.method === 'thread/compacted'
}

/** Claude's microcompaction boundary: `{type:system,subtype:microcompact_boundary}`. */
function isMicrocompactBoundary(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'microcompact_boundary'
}

/**
 * Handles compacting status notifications. The canonical shape is the raw
 * Claude `system` message: `{type:"system",subtype:"status",status:"compacting"}`.
 * Routes through CompactionDivider (with the spinner) so the standalone and
 * notification-thread compacting rows render identical markup.
 */
export const compactingRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || !isCompactingStatus(parsed))
      return null
    return <CompactionDivider text={COMPACTING_LABEL} loading />
  },
}

/**
 * Handles compact_boundary messages. Recognizes two shapes:
 *  - Claude raw `system` message: `{type:"system",subtype:"compact_boundary",compact_metadata:{trigger,pre_tokens,post_tokens?,tokens_saved?}}`
 *    (the camelCase `compactMetadata`/`preTokens`/`postTokens`/`tokensSaved` forms are accepted too)
 *  - Codex raw JSON-RPC notification: `{method:"thread/compacted",params:{threadId,turnId}}` (no token metadata today)
 */
export const compactBoundaryRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || !isCompactBoundary(parsed))
      return null
    // Claude carries metadata under compact_metadata/compactMetadata; Codex
    // carries none today (the message itself is the boundary).
    return <CompactionDivider text={compactBoundaryLabel(parsed)} />
  },
}

/**
 * Handles microcompact_boundary messages: `{"type":"system","subtype":"microcompact_boundary"}`.
 * Claude Code emits no metadata object for microcompaction (no token counts or
 * trigger on the wire), so this renders a plain label with no detail.
 */
export const microcompactBoundaryRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || !isMicrocompactBoundary(parsed))
      return null
    return <CompactionDivider text={MICROCOMPACT_LABEL} />
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

/**
 * A single entry produced by `threadEntriesFor` before grouping is resolved.
 * Aliased to the registry's `NotificationThreadEntry` -- the exact type a
 * provider plugin's `notificationThreadEntry` returns -- so the plugin pre-pass
 * and the shared switch are provably the same shape and cannot desync.
 */
type ThreadEntry = NotificationThreadEntry

/** What survives into the final render: `group` entries are flushed to `text`. */
type RenderEntry = Exclude<ThreadEntry, { kind: 'group' }>

/** Build a one-element text thread entry. */
function textEntry(text: string): ThreadEntry[] {
  return [{ kind: 'text', text }]
}

/** Build a one-element divider thread entry (with the spinner when `loading`). */
function dividerEntry(text: string, loading?: boolean): ThreadEntry[] {
  return [{ kind: 'divider', text, loading }]
}

/**
 * Walk a single message in a notification_thread wrapper, producing one or
 * more flat thread entries via the provider plugin (if any) followed by the
 * provider-neutral switch. Returns an array — the caller appends.
 */
function threadEntriesFor(
  m: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
): ThreadEntry[] {
  const plugin = agentProvider != null ? providerFor(agentProvider) : undefined
  const fromPlugin = plugin?.notificationThreadEntry?.(m)
  if (fromPlugin !== null && fromPlugin !== undefined)
    return fromPlugin

  const t = m.type as string | undefined
  const st = m.subtype as string | undefined

  if (t === NOTIFICATION_TYPE.SettingsChanged) {
    const parts = formatSettingsChanges(m.changes)
    return parts.length > 0 ? textEntry(parts.join(', ')) : []
  }
  if (t === NOTIFICATION_TYPE.ContextCleared)
    return textEntry(CONTEXT_CLEARED_LABEL)
  if (t === NOTIFICATION_TYPE.PlanExecution)
    return textEntry('Executing plan')
  if (t === NOTIFICATION_TYPE.AgentError)
    return textEntry(pickString(m, 'error', UNKNOWN_ERROR_LABEL))
  if (t === NOTIFICATION_TYPE.Interrupted)
    return textEntry(INTERRUPTED_LABEL)
  if (t === NOTIFICATION_TYPE.PlanUpdated) {
    const label = planUpdatedLabel(m)
    return label !== null ? textEntry(label) : []
  }
  if (t === 'system' && st === 'api_retry')
    return textEntry(formatApiRetryLabel(m))
  if (isCompactingStatus(m))
    return dividerEntry(COMPACTING_LABEL, true)
  // Claude `compact_boundary` and Codex `thread/compacted` are both the
  // completed-boundary signal; route both through the shared label so they stay
  // in lockstep with the standalone renderer. Codex carries no metadata today
  // but would pick up detail automatically if it ever adds it.
  if (isCompactBoundary(m))
    return dividerEntry(compactBoundaryLabel(m))
  // Codex `item/started` of a contextCompaction item is the in-progress
  // spinner. The completed boundary arrives later via `thread/compacted`.
  if (m.method === 'item/started') {
    const params = pickObject(m, 'params')
    const item = pickObject(params, 'item')
    if (item && item.type === 'contextCompaction')
      return dividerEntry(COMPACTING_LABEL, true)
  }
  if (isMicrocompactBoundary(m))
    return dividerEntry(MICROCOMPACT_LABEL)
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
    elements.push(<CompactionDivider text={entry.text} loading={entry.loading} />)
  }
  flushPendingText()

  if (elements.length === 0)
    return null

  if (elements.length === 1)
    return elements[0]

  return <div>{elements}</div>
}

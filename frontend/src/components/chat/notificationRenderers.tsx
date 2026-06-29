/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSXElement } from 'solid-js'
import type { MessageContentRenderer } from './messageRenderers'
import type { NotificationThreadEntry } from './providers/registry'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { CompactionDetail } from '~/lib/messageParser'
import ArrowDownToLine from 'lucide-solid/icons/arrow-down-to-line'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Icon } from '~/components/common/Icon'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { isCompactBoundary, parseBoundaryMeta, toTokenCount } from '~/lib/messageParser'
import { NOTIFICATION_TYPE } from '~/lib/notificationTypes'
import { getCachedSettingsGroupLabel, getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { spinner } from '~/styles/animations.css'
import { MarkdownText } from './messageRenderers'
import {
  controlResponseMessage,
  resultDivider,
} from './messageStyles.css'
import { pluginFor } from './providers/registry'
import { formatTokenCount } from './rendererUtils'
import { OPTION_ID_PERMISSION_MODE } from './settingsGroups'

// Provider-neutral notification labels used by the notification-thread switch
// (`threadEntriesFor`). Hoisted to named constants so the wording lives in one
// place and is referenced by name -- the same as COMPACTING_LABEL /
// MICROCOMPACT_LABEL for the compaction rows below.
const CONTEXT_CLEARED_LABEL = 'Context cleared'
const INTERRUPTED_LABEL = 'Interrupted'
const UNKNOWN_ERROR_LABEL = 'Unknown error'

// ---------------------------------------------------------------------------
// Display helpers for settings change notifications
// ---------------------------------------------------------------------------

function displayLabel(provider: AgentProvider | undefined, key: string): string {
  // Prefer the per-provider cached group label so a provider that relabels a well-known
  // axis is honored (Pi labels its effort axis "Thinking Level"), then fall back to the
  // canonical English name. The well-known fallbacks keep a historical notification
  // readable when the cache hasn't been primed for that provider yet.
  return getCachedSettingsGroupLabel(provider, key) ?? wellKnownAxisLabel(key)
}

function wellKnownAxisLabel(key: string): string {
  switch (key) {
    case 'model': return 'Model'
    case 'effort': return 'Effort'
    case OPTION_ID_PERMISSION_MODE: return 'Permission Mode'
    default: return key
  }
}

function displayValue(provider: AgentProvider | undefined, key: string, value: string): string {
  return getCachedSettingsLabel(provider, key, value) ?? value
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
function parseSettingsChanges(changes: unknown, provider: AgentProvider | undefined): SettingsChange[] {
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
      label: pickString(val, 'label', undefined) ?? displayLabel(provider, key),
      // Gate the "(new)"-only form on the absence of an old VALUE, not on an
      // empty old DISPLAY: a real value whose display resolves to "" is still a
      // transition and must keep the arrow.
      firstSet: oldValue === '',
      oldDisplay: pickString(val, 'oldLabel', undefined) ?? displayValue(provider, key, oldValue),
      newDisplay: pickString(val, 'newLabel', undefined) ?? displayValue(provider, key, newValue),
    })
  }
  return result
}

/**
 * Format settings changes as `Label (old → new)` parts, degrading to
 * `Label (new)` when there is no old value. Used by the notification-thread
 * switch (`threadEntriesFor`) to render settings_changed notifications. Labels are
 * resolved against the agent's provider-scoped label cache.
 */
function formatSettingsChanges(changes: unknown, provider: AgentProvider | undefined): string[] {
  return parseSettingsChanges(changes, provider).map(c =>
    c.firstSet ? `${c.label} (${c.newDisplay})` : `${c.label} (${c.oldDisplay} → ${c.newDisplay})`,
  )
}

/**
 * Build the plan_updated label, or null when there is no title to show. Two
 * variants: "Plan updated and renamed to <title>" when `update_agent_title` is
 * set, else "Plan updated: <title>". Used by the notification-thread switch
 * (`threadEntriesFor`).
 */
function planUpdatedLabel(source: Record<string, unknown>): string | null {
  const title = pickString(source, 'plan_title')
  if (!title)
    return null
  return source.update_agent_title === true
    ? `Plan updated and renamed to ${title}`
    : `Plan updated: ${title}`
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

// ---------------------------------------------------------------------------
// Context compaction boundary renderers
// ---------------------------------------------------------------------------

/**
 * A compaction divider row: a leading icon (the down-to-line arrow, or a
 * spinner while compaction is in progress) followed by the label. Rendered by
 * `renderNotificationThread` for every `divider` entry, so a boundary looks the
 * same on its own or consolidated, and across providers -- a provider that
 * surfaces its own compaction event (e.g. Pi) supplies a `divider` entry that
 * flows through here, getting the same icon + layout without re-implementing it.
 */
function CompactionDivider(props: { text: string, loading?: boolean }): JSXElement {
  return (
    <div class={resultDivider}>
      <Icon icon={props.loading ? LoaderCircle : ArrowDownToLine} size="sm" class={props.loading ? spinner : undefined} />
      {` ${props.text}`}
    </div>
  )
}

// Boundary labels with no per-message detail. COMPACTING_LABEL is exported
// because the Pi renderer imports it so its compaction label matches; both are
// referenced by name from the notification-thread switch (the same as
// compactBoundaryLabel for the compacted boundary). The microcompact label has
// no detail because Claude Code emits no microcompact metadata object.
export const COMPACTING_LABEL = 'Compacting context...'
const MICROCOMPACT_LABEL = 'Context microcompacted'

// Compaction metadata parsing (parseBoundaryMeta, toTokenCount, the
// CompactionDetail shape, the isCompactBoundary predicate) lives in messageParser
// beside the other wire extractors, since the context-usage grid consumes the
// same parse to refresh on compaction. This file keeps only the formatting that
// turns a CompactionDetail into a display label. microcompact_boundary carries no
// metadata (Claude Code emits none), so that boundary renders a plain label.

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
 * {@link compactedLabel}. Used by the notification-thread switch; centralizes the
 * label and metadata-key resolution.
 */
function compactBoundaryLabel(source: Record<string, unknown>): string {
  return compactedLabel(parseBoundaryMeta(source))
}

// Recognition predicates for the compaction message shapes, used by the
// notification-thread switch (`threadEntriesFor`). Each names WHICH wire shapes
// count as a given boundary so the switch reads declaratively instead of
// hand-repeating the type/subtype/method checks inline. The completed-boundary
// predicate (isCompactBoundary, matching Claude's and Codex's two shapes) lives
// in messageParser since the grid extractor recognizes the same signal.

/** Claude's in-progress compacting status: `{type:system,subtype:status,status:compacting}`. */
function isCompactingStatus(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'status' && m.status === NOTIFICATION_TYPE.Compacting
}

/** Claude's microcompaction boundary: `{type:system,subtype:microcompact_boundary}`. */
function isMicrocompactBoundary(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'microcompact_boundary'
}

/** Handles control response messages: {"isSynthetic":true,"controlResponse":{"action":"approved"|"rejected","comment":"..."}} */
export const controlResponseRenderer: MessageContentRenderer = {
  render(parsed, context) {
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
            <MarkdownText text={comment} context={context} />
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
 * Walk a single notification message (whether on its own or one entry of a
 * consolidated thread), producing one or more flat thread entries via the
 * provider plugin (if any) followed by the provider-neutral switch. Returns an
 * array — the caller appends.
 */
function threadEntriesFor(
  m: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
): ThreadEntry[] {
  const plugin = pluginFor(agentProvider)
  const fromPlugin = plugin?.notificationThreadEntry?.(m)
  if (fromPlugin !== null && fromPlugin !== undefined)
    return fromPlugin

  const t = m.type as string | undefined
  const st = m.subtype as string | undefined

  if (t === NOTIFICATION_TYPE.SettingsChanged) {
    const parts = formatSettingsChanges(m.changes, agentProvider)
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
  // completed-boundary signal; route both through the shared label. Codex carries
  // no metadata today but would pick up detail automatically if it ever adds it.
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
 * Renders a list of notification messages as a single combined notification --
 * the sole render path for the `notification` category, whether a standalone
 * notification (a one-element list) or a consolidated thread (Hub threads
 * consecutive notifications into one `notification_thread` wire wrapper).
 * Returns null when nothing renders.
 *
 * `agentProvider` is consulted via `plugin.notificationThreadEntry` for any
 * provider-specific messages (e.g. Codex MCP startup statuses) before the
 * shared switch handles provider-neutral types.
 */
/**
 * Pure: the ordered render entries (text + divider) a notification thread produces,
 * AFTER group coalescing (a run of same-key `group` entries collapses into one
 * `Prefix: a, b, c` text entry). The single source of truth for both
 * `renderNotificationThread` and `notificationThreadMetrics`, so the two can't
 * drift on WHICH children render or WHAT they say.
 */
export function notificationThreadEntries(messages: unknown[], agentProvider?: AgentProvider): RenderEntry[] {
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
  return entries
}

/**
 * Pure body metrics for notification rendering: the total rendered text length and
 * the number of laid-out BLOCKS the thread becomes. `renderNotificationThread`
 * coalesces a run of consecutive `text` entries into ONE comma-joined paragraph
 * `<div>` and renders each `divider` as its own block, so a many-child thread with a
 * short joined body lays out as a few wrapped lines -- NOT one line per child. Sizing
 * from this (vs the child count) is what keeps collapsed-row logic from treating a
 * coalesced thread as one row per child.
 */
export function notificationThreadMetrics(messages: unknown[], agentProvider?: AgentProvider): { textLength: number, blockCount: number } {
  const entries = notificationThreadEntries(messages, agentProvider)
  let textLength = 0
  let blockCount = 0
  let inTextRun = false
  for (const e of entries) {
    if (e.kind === 'text') {
      // Consecutive text entries join with ', ' into one paragraph; count the run
      // as a single block and add the 2-char joiner for every entry after the first.
      textLength += e.text.length + (inTextRun ? 2 : 0)
      if (!inTextRun) {
        blockCount++
        inTextRun = true
      }
    }
    else {
      textLength += e.text.length
      blockCount++
      inTextRun = false
    }
  }
  return { textLength, blockCount }
}

export function renderNotificationThread(messages: unknown[], agentProvider?: AgentProvider): JSXElement {
  const entries = notificationThreadEntries(messages, agentProvider)

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

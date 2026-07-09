import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import { decompressContentToString } from '~/lib/decompress'
import { isObject, pickFirstNumber, pickFirstObject, pickNumber, pickString } from '~/lib/jsonPick'

/**
 * Content-type discriminator emitted by the backend's `wrapNotifContent`
 * for every notification-thread row. Match this constant in lockstep with
 * `notifThreadWrapperType` in `backend/internal/worker/service/output.go`.
 */
export const NOTIFICATION_THREAD_TYPE = 'notification_thread'

/**
 * The result of parsing a compressed AgentChatMessage. Every field is
 * derived from a single decompress-then-JSON.parse pass.
 */
export interface ParsedMessageContent {
  /** The raw decompressed text (for "Copy Raw JSON"). */
  rawText: string
  /** The top-level parsed JSON object, or null on parse failure. */
  topLevel: Record<string, unknown> | null
  /** The first (parent) inner message object, or undefined. */
  parentObject: Record<string, unknown> | undefined
  /** The notification wrapper envelope if this is a notification thread, null otherwise. */
  wrapper: { old_seqs: number[], messages: unknown[] } | null
}

const EMPTY_PARSED: ParsedMessageContent = {
  rawText: '',
  topLevel: null,
  parentObject: undefined,
  wrapper: null,
}

// AgentChatMessage is immutable once persisted, so caching by message
// reference avoids the repeated decompress + JSON.parse cost across
// every caller of parseMessageContent (isAgentWorking scans, the
// MessageBubble render path, the to-do extractor, etc.). The WeakMap
// lets trimmed/replaced messages get GC'd without manual eviction.
const parseCache = new WeakMap<AgentChatMessage, ParsedMessageContent>()

/**
 * Decompress and parse an AgentChatMessage's content in a single pass.
 * Never throws -- returns safe defaults on any failure.
 *
 * Notification-threaded messages use the wrapper format:
 *   {"type":"notification_thread","old_seqs":[...],"messages":[{...},...]}
 * Detection is purely shape-based — `type === 'notification_thread'`
 * uniquely identifies the wrapper, decoupled from the persisted source.
 * All other messages are stored as raw JSON (no wrapper).
 */
export function parseMessageContent(message: AgentChatMessage): ParsedMessageContent {
  const cached = parseCache.get(message)
  if (cached)
    return cached
  const result = parseMessageContentImpl(message)
  parseCache.set(message, result)
  return result
}

/**
 * Drop the memoized parse for a message whose content was replaced IN PLACE under a
 * stable reference -- the store's same-seq update merges new content into the
 * existing proxy, keeping its reference. That breaks the by-reference immutability
 * assumption above, so the mutator MUST evict here or every caller keeps seeing the
 * pre-update parse. Safe no-op when the message was never parsed.
 */
export function invalidateMessageParseCache(message: AgentChatMessage): void {
  parseCache.delete(message)
}

function parseMessageContentImpl(message: AgentChatMessage): ParsedMessageContent {
  const text = decompressContentToString(message.content, message.contentCompression)
  if (text === null)
    return EMPTY_PARSED

  try {
    const obj = JSON.parse(text)

    // Notification-threaded messages are identified by their explicit
    // `type: "notification_thread"` discriminator. The discriminator is
    // emitted by the backend's wrapNotifContent for every notification
    // thread row regardless of source (AGENT or LEAPMUX), so the parser
    // does not need to look at message.source.
    if (obj?.type === NOTIFICATION_THREAD_TYPE && Array.isArray(obj.messages)) {
      const wrapper = { old_seqs: obj.old_seqs ?? [], messages: obj.messages }

      if (obj.messages.length === 0)
        return { rawText: text, topLevel: obj, parentObject: undefined, wrapper }

      const first = obj.messages[0]
      const parent = (typeof first === 'object' && first !== null && !Array.isArray(first))
        ? first as Record<string, unknown>
        : undefined
      return {
        rawText: text,
        topLevel: obj,
        parentObject: parent,
        wrapper,
      }
    }

    // Regular messages: stored as raw JSON, no wrapper.
    const parent = (typeof obj === 'object' && obj !== null && !Array.isArray(obj))
      ? obj as Record<string, unknown>
      : undefined
    return { rawText: text, topLevel: obj, parentObject: parent, wrapper: null }
  }
  catch {
    return { rawText: text, topLevel: null, parentObject: undefined, wrapper: null }
  }
}

// ---------------------------------------------------------------------------
// Inner message accessors
// ---------------------------------------------------------------------------

/**
 * Get the unwrapped inner message -- the first message if wrapped,
 * or the top-level object if not. Replaces the
 * `parsed?.messages?.[0] ?? parsed` pattern.
 */
export function getInnerMessage(parsed: ParsedMessageContent): Record<string, unknown> | null {
  return parsed.parentObject ?? parsed.topLevel
}

/**
 * Get the inner message type string (e.g. 'assistant', 'context_cleared', 'rate_limit').
 */
export function getInnerMessageType(parsed: ParsedMessageContent): string | undefined {
  const inner = getInnerMessage(parsed)
  return inner?.type as string | undefined
}

/**
 * The raw provider `message.usage` bag, when present. The `.message.usage` LOCATION is a
 * provider-neutral envelope shape -- Claude and Pi both carry per-message token usage there -- so
 * this accessor stays neutral; only the field NAMES inside are provider-specific (Claude
 * `input_tokens`/`cache_*`, Pi `input`/`cacheWrite`), which each provider's `contextUsageFromMessage`
 * interprets after reading the bag through here. Returns undefined when the message carries none.
 */
export function messageUsage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const message = getInnerMessage(parsed)?.message
  return isObject(message) ? (message.usage as Record<string, unknown> | undefined) : undefined
}

/**
 * The inner messages to scan for a notification: a consolidated thread's wrapped
 * messages, or the lone top-level message as a one-element array (empty when the
 * content failed to parse). Several extractors walk this same shape -- in reverse
 * when the most recent matching entry should win.
 */
export function messagesOf(parsed: ParsedMessageContent): unknown[] {
  return parsed.wrapper
    ? parsed.wrapper.messages
    : parsed.topLevel ? [parsed.topLevel] : []
}

// ---------------------------------------------------------------------------
// Domain-specific extractors
// ---------------------------------------------------------------------------

/** Convert todo items to a markdown checklist string. */
export function todosToMarkdown(items: ReadonlyArray<{ status: string, content: string }>): string {
  return items.map((t) => {
    switch (t.status) {
      case 'completed': return `- [x] ${t.content}`
      case 'in_progress': return `- [~] ${t.content}`
      case 'deleted': return `- [-] ~~${t.content}~~`
      default: return `- [ ] ${t.content}`
    }
  }).join('\n')
}

/** Normalize a snake_case context_usage broadcast payload into AgentSessionInfo shape. */
export function normalizeContextUsage(value: unknown): ContextUsageInfo | undefined {
  if (!isObject(value))
    return undefined

  const inputTokens = pickNumber(value, 'input_tokens', 0)
  const cacheCreationInputTokens = pickNumber(value, 'cache_creation_input_tokens', 0)
  const cacheReadInputTokens = pickNumber(value, 'cache_read_input_tokens', 0)
  const outputTokens = pickNumber(value, 'output_tokens', undefined)
  // Pi's native RPC shape calls this `tokens`; LeapMux-normalized payloads use
  // `context_tokens` so the grid can distinguish authoritative totals from the
  // Claude-style input/cache component fields.
  const contextTokens = pickFirstNumber(value, ['context_tokens', 'tokens'])
  const contextWindow = pickNumber(value, 'context_window', undefined)

  const hasTokenData = inputTokens > 0
    || cacheCreationInputTokens > 0
    || cacheReadInputTokens > 0
    || (outputTokens ?? 0) > 0
    || (contextTokens ?? 0) > 0
  if (!hasTokenData)
    return undefined

  const usage: ContextUsageInfo = {
    inputTokens,
    cacheCreationInputTokens,
    cacheReadInputTokens,
  }
  if (outputTokens !== undefined)
    usage.outputTokens = outputTokens
  if (contextTokens !== undefined)
    usage.contextTokens = contextTokens
  if (contextWindow !== undefined && contextWindow > 0)
    usage.contextWindow = contextWindow
  return usage
}

/**
 * Extract usage metadata (context usage + cumulative cost) from a message. The provider-neutral
 * fields are read here -- subagent skip (a subagent's usage is already in the parent's totals),
 * `total_cost_usd`, and a backend-normalized `context_usage`. Only when no normalized context_usage
 * is present does it fall through to the provider's `contextUsageFromMessage`, which reads whatever
 * raw shape carries the usage (Codex `thread/tokenUsage/updated`, Claude/Pi `message.usage`) -- so
 * no provider wire shape lives here and the neutral guards never live in a provider. Runs for every
 * message in the notification-metadata pass; returns null for a message that carries no usage.
 */
export function extractContextUsage(
  parsed: ParsedMessageContent,
  contextUsageFromMessage: (parsed: ParsedMessageContent) => ContextUsageInfo | null,
): {
  totalCostUsd?: number
  contextUsage?: ContextUsageInfo
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return null
  // Skip subagent messages — their usage is already included in the parent's totals.
  if (inner.parent_tool_use_id)
    return null
  const result: { totalCostUsd?: number, contextUsage?: ContextUsageInfo } = {}

  const totalCostUsd = pickNumber(inner, 'total_cost_usd', undefined)
  if (totalCostUsd !== undefined)
    result.totalCostUsd = totalCostUsd

  const normalizedContextUsage = normalizeContextUsage(inner.context_usage)
  if (normalizedContextUsage) {
    result.contextUsage = normalizedContextUsage
  }
  else {
    const fromProvider = contextUsageFromMessage(parsed)
    if (fromProvider)
      result.contextUsage = fromProvider
  }

  return Object.keys(result).length > 0 ? result : null
}

// ---------------------------------------------------------------------------
// Context compaction metadata
// ---------------------------------------------------------------------------

// compact_boundary carries its metadata under a snake_case key in the SDK
// stream-json output (compact_metadata) or a camelCase key in the .jsonl
// transcript form (compactMetadata); resolve against both from one definition
// so the accepted key list lives in one place. The order is immaterial -- a
// message carries one casing, and pickFirstObject returns the first present.
// Internal: callers resolve metadata through parseBoundaryMeta, not this list.
const COMPACT_META_KEYS = ['compact_metadata', 'compactMetadata'] as const

/**
 * Normalized, provider-agnostic compaction detail. The shared parser turns raw
 * wire metadata into this (see {@link parseCompactionMeta}); a provider that
 * surfaces its own compaction event (e.g. Pi) constructs it directly. The
 * notification-thread formatter and the context-usage grid both consume this
 * typed shape, so they never touch raw snake/camelCase wire keys and a provider
 * can't accidentally couple to them.
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
 * Coerce a raw numeric token count to a usable value: finite and non-negative.
 * Non-finite inputs (NaN/Infinity -- which JSON can't carry but a synthesized
 * payload could) degrade to undefined; negatives clamp to 0, so a provider
 * reporting an explicit negative count (or a derived `pre - saved` where
 * saved > pre) yields 0 instead of a negative size.
 */
export function toTokenCount(n: number | undefined): number | undefined {
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
 * sanitization is deferred to the consumer (both the label formatter and the
 * grid extractor clamp via {@link toTokenCount}), so every caller -- wire-derived
 * or provider-synthesized -- gets the same treatment. Internal: external callers
 * resolve a raw boundary via {@link parseBoundaryMeta}.
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
 * A completed compaction boundary: Claude's `compact_boundary` system message or Codex's
 * `thread/compacted` JSON-RPC notification -- both the same signal. Deliberately NEUTRAL and
 * shape-based (not a per-provider hook): the notification-thread renderer and the context-usage
 * grid recognize a boundary by SHAPE regardless of the row's provider, so legacy Codex rows still
 * carrying the `compact_boundary` shape, and any cross-provider row, render correctly. This is the
 * shared renderer's cross-provider boundary vocabulary, a sibling of {@link parseBoundaryMeta}'s
 * shared metadata-key knowledge, not one provider's wire parsing.
 */
export function isCompactBoundary(m: Record<string, unknown>): boolean {
  return (m.type === 'system' && m.subtype === 'compact_boundary') || m.method === 'thread/compacted'
}

/**
 * Resolve a raw boundary message's compaction metadata (under the snake_case
 * `compact_metadata` or camelCase `compactMetadata` key) into a
 * {@link CompactionDetail}. The single place that knows where a boundary carries
 * its metadata, shared by the grid extractor here and the notification-thread
 * label formatter, so the key resolution can't drift between them.
 */
export function parseBoundaryMeta(m: Record<string, unknown>): CompactionDetail {
  return parseCompactionMeta(pickFirstObject(m, COMPACT_META_KEYS))
}

/**
 * Resolve the post-compaction context size (in tokens) from a notification, for
 * refreshing the context-usage grid the instant a boundary lands -- rather than
 * leaving the now-stale pre-compaction usage on screen until the next
 * assistant/result message overwrites it. Scans the wrapper's messages (or the
 * lone top-level message) in reverse so the most recent boundary in a
 * consolidated thread wins, and returns the sanitized post count of the most
 * recent boundary that carries a resolvable one (skipping boundaries that don't).
 *
 * Returns undefined when there is no boundary, or no boundary carries a
 * resolvable post (`post_tokens` absent and no `pre - tokens_saved` to derive it
 * from) -- e.g. Codex's `thread/compacted`, which carries no metadata today.
 * The caller leaves the grid untouched in that case.
 */
export function extractCompactionContextTokens(parsed: ParsedMessageContent): number | undefined {
  const messages = messagesOf(parsed)
  // Reverse so the most recent boundary wins. Skip a boundary whose post is
  // unresolvable (e.g. Codex's metadata-less thread/compacted) and keep scanning
  // so an earlier boundary that does carry a post can still refresh the grid,
  // rather than bailing out on the first boundary encountered.
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    if (!isObject(msg) || !isCompactBoundary(msg))
      continue
    const post = toTokenCount(parseBoundaryMeta(msg).post)
    if (post !== undefined)
      return post
  }
  return undefined
}

function modelContextWindow(modelData: unknown): number {
  if (!modelData || typeof modelData !== 'object')
    return 0
  const cw = (modelData as Record<string, unknown>).contextWindow
  return typeof cw === 'number' && cw > 0 ? cw : 0
}

function maxContextWindow(modelUsage: Record<string, unknown>): number {
  let max = 0
  for (const modelData of Object.values(modelUsage))
    max = Math.max(max, modelContextWindow(modelData))
  return max
}

function findPrimaryContextWindow(modelUsage: Record<string, unknown>, primaryModelId?: string): number {
  if (!primaryModelId)
    return maxContextWindow(modelUsage)

  let family = primaryModelId
  let suffix = ''
  const bracketIdx = primaryModelId.indexOf('[')
  if (bracketIdx >= 0) {
    family = primaryModelId.slice(0, bracketIdx)
    suffix = primaryModelId.slice(bracketIdx)
  }

  for (const [key, modelData] of Object.entries(modelUsage)) {
    if (!key.includes(family))
      continue
    if (suffix) {
      if (!key.includes(suffix))
        continue
    }
    else if (key.includes('[')) {
      continue
    }

    const cw = modelContextWindow(modelData)
    if (cw > 0)
      return cw
  }

  return maxContextWindow(modelUsage)
}

/**
 * Extract result-message metadata: subtype, context usage/window, totalCostUsd, numToolUses. The
 * neutral fields are read here; `subtypeFallback` is the provider's derivation
 * (Provider.resultSubtype) for a subtype that isn't on `inner.subtype` (Codex's `turn.status`), so
 * no provider wire shape is matched in shared code. `subtypeFallback` takes the whole parsed message
 * (like the sibling provider hooks) and is required so a caller can't silently drop the provider's
 * subtype derivation; pass `() => undefined` when the caller has no provider fallback.
 */
export function extractResultMetadata(
  parsed: ParsedMessageContent,
  primaryModelId: string | undefined,
  subtypeFallback: (parsed: ParsedMessageContent) => string | undefined,
): {
  subtype?: string
  contextWindow?: number
  contextUsage?: ContextUsageInfo
  totalCostUsd?: number
  numToolUses?: number
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return null
  // Skip subagent messages — their usage is already included in the parent's totals.
  if (inner.parent_tool_use_id)
    return null

  const result: { subtype?: string, contextWindow?: number, contextUsage?: ContextUsageInfo, totalCostUsd?: number, numToolUses?: number } = {}

  if (inner.subtype)
    result.subtype = inner.subtype as string

  // A provider whose terminal envelope carries no `subtype` (Codex maps turn.status) derives it.
  if (!result.subtype) {
    const fallback = subtypeFallback(parsed)
    if (fallback)
      result.subtype = fallback
  }

  // num_tool_uses is injected by the backend for all providers.
  if (typeof inner.num_tool_uses === 'number')
    result.numToolUses = inner.num_tool_uses as number

  if (inner.modelUsage && typeof inner.modelUsage === 'object') {
    const cw = findPrimaryContextWindow(inner.modelUsage as Record<string, unknown>, primaryModelId)
    if (cw > 0)
      result.contextWindow = cw
  }

  const normalizedContextUsage = normalizeContextUsage(inner.context_usage)
  if (normalizedContextUsage)
    result.contextUsage = normalizedContextUsage

  const totalCostUsd = pickNumber(inner, 'total_cost_usd', undefined)
  if (totalCostUsd !== undefined)
    result.totalCostUsd = totalCostUsd

  return Object.keys(result).length > 0 ? result : null
}

/** Extract settings changes from a LEAPMUX settings_changed inner message. */
export function extractSettingsChanges(parsed: ParsedMessageContent): {
  [key: string]: { old: string, new: string } | undefined
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.type !== 'settings_changed')
    return null
  const changes = inner.changes as Record<string, unknown> | undefined
  if (!changes || typeof changes !== 'object')
    return null
  return changes as { [key: string]: { old: string, new: string } | undefined }
}

/**
 * Plan-update payload extracted from a `plan_updated` LEAPMUX notification.
 * `updateAgentTitle === true` signals the backend's auto-rename branch
 * fired and the agent tab name should be updated to `planTitle`.
 */
export interface PlanUpdatedInfo {
  planTitle: string
  planFilePath: string
  updateAgentTitle: boolean
}

/**
 * Extract plan_updated payload from a notification (wrapped or unwrapped).
 * Returns the most recent `plan_updated` entry in the wrapper, or undefined
 * if none present.
 */
export function extractPlanUpdated(parsed: ParsedMessageContent): PlanUpdatedInfo | undefined {
  const messages = messagesOf(parsed)
  // Iterate in reverse so the most recent entry in a consolidated thread wins.
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    if (typeof msg === 'object' && msg !== null) {
      const m = msg as Record<string, unknown>
      if (m.type === 'plan_updated') {
        return {
          planTitle: typeof m.plan_title === 'string' ? m.plan_title : '',
          planFilePath: typeof m.plan_file_path === 'string' ? m.plan_file_path : '',
          updateAgentTitle: m.update_agent_title === true,
        }
      }
    }
  }
  return undefined
}

/** Extract plan file path from a plan_execution message (wrapped or unwrapped). */
export function extractPlanFilePath(parsed: ParsedMessageContent): string | undefined {
  // Check all messages in the wrapper (or the top-level object).
  for (const msg of messagesOf(parsed)) {
    if (typeof msg === 'object' && msg !== null) {
      const m = msg as Record<string, unknown>
      if (m.type === 'plan_execution' && typeof m.plan_file_path === 'string' && m.plan_file_path !== '') {
        return m.plan_file_path as string
      }
    }
  }
  return undefined
}

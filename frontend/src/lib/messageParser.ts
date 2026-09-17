import type { AgentChatMessage, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/models/agentSession'
import { CONTEXT_USAGE_FIELD, SESSION_INFO_KEY } from '~/generated/contracts/session-info'
import { MESSAGE_SUPPLEMENT_FIELD, NOTIFICATION_FIELD, NOTIFICATION_THREAD_TYPE, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { decompressContent } from '~/lib/decompress'
import { isObject, pickFirstNumber, pickNumber, pickString } from '~/lib/jsonPick'

/**
 * Content-type discriminator emitted by the backend's `wrapNotifContent`
 * for every notification-thread row. Both this constant and the
 * notification-type tokens compared below come from
 * contracts/worker-vocab.json (~/generated/contracts/worker-vocab); the
 * worker stamps the same tokens via its generated Go constants.
 */

/**
 * The result of parsing a compressed AgentChatMessage. Every field is
 * derived from a single decompress-then-JSON.parse pass.
 */
export interface ParsedMessageContent {
  /** The raw decompressed text (for "Copy Raw JSON"). */
  rawText: string
  /** The original bytes could not be decoded. They remain on the message. */
  contentDecodeFailed?: boolean
  /** Completion belongs to LeapMux, outside the provider payload. */
  completion?: MessageCompletion
  /** LeapMux data from a separate field. The provider payload remains unchanged. */
  supplementalContent?: unknown
  /** Worker-calculated metadata uses its own schema, separate from recovered provider data. */
  messageMetadata?: unknown
  /** Decoded supplemental text for the Raw JSON view, including invalid JSON. */
  supplementalRawText?: string
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
// every caller of parseMessageContent (the MessageBubble render path,
// the to-do extractor, the result-divider hook, etc.). The WeakMap
// lets trimmed/replaced messages get GC'd without manual eviction.
const parseCache = new WeakMap<AgentChatMessage, ParsedMessageContent>()

// Raw JSON must retain byte order marks and must not replace invalid UTF-8 bytes.
const messageTextDecoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true })

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
  let result = parseMessageContentImpl(message)
  if (message.supplementalContent?.length) {
    const text = readMessageText(message.supplementalContent, message.supplementalContentCompression)
    if (text !== null) {
      let supplementalContent: unknown
      try {
        supplementalContent = JSON.parse(text)
      }
      catch {
        // Invalid supplemental JSON must not prevent the original message from rendering.
      }
      result = {
        ...result,
        supplementalContent: isObject(supplementalContent) ? supplementalContent[MESSAGE_SUPPLEMENT_FIELD.Provider] : undefined,
        messageMetadata: isObject(supplementalContent) ? supplementalContent[MESSAGE_SUPPLEMENT_FIELD.Metadata] : undefined,
        supplementalRawText: text,
      }
    }
  }
  if (message.completion)
    result = { ...result, completion: message.completion }
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
  const text = readMessageText(message.content, message.contentCompression)
  if (text === null)
    return { ...EMPTY_PARSED, contentDecodeFailed: true }

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

function readMessageText(content: Uint8Array, compression: AgentChatMessage['contentCompression']): string | null {
  try {
    const bytes = decompressContent(content, compression)
    return bytes === null ? null : messageTextDecoder.decode(bytes)
  }
  catch {
    // A damaged compressed field must not hide the other message fields.
    return null
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

  const inputTokens = pickNumber(value, CONTEXT_USAGE_FIELD.InputTokens, 0)
  const cacheCreationInputTokens = pickNumber(value, CONTEXT_USAGE_FIELD.CacheCreationInputTokens, 0)
  const cacheReadInputTokens = pickNumber(value, CONTEXT_USAGE_FIELD.CacheReadInputTokens, 0)
  const outputTokens = pickNumber(value, CONTEXT_USAGE_FIELD.OutputTokens, undefined)
  // Pi's native RPC shape calls this `tokens`; LeapMux-normalized payloads use
  // `context_tokens` so the grid can distinguish authoritative totals from the
  // Claude-style input/cache component fields. Only the LeapMux spelling comes
  // from the contract -- `tokens` is Pi's own protocol token, which LeapMux does
  // not own (see the _readme in contracts/session-info.json).
  const contextTokens = pickFirstNumber(value, [CONTEXT_USAGE_FIELD.ContextTokens, 'tokens'])
  const contextWindow = pickNumber(value, CONTEXT_USAGE_FIELD.ContextWindow, undefined)

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

  const totalCostUsd = pickNumber(inner, SESSION_INFO_KEY.TotalCostUsd, undefined)
  if (totalCostUsd !== undefined)
    result.totalCostUsd = totalCostUsd

  const normalizedContextUsage = normalizeContextUsage(inner[SESSION_INFO_KEY.ContextUsage])
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
 * The SESSION metadata a turn-end message carries: the context window, the normalized
 * context usage, and the running cost. Every field is provider-neutral, injected by the
 * worker, so no provider wire shape is matched here.
 *
 * The turn's OWN totals are not this function's business. `num_tool_uses`,
 * `total_cost_usd` and `duration_ms` describe the turn a reader looks at, and
 * `dividerMetaFromMessage` states them on that row. This used to return the tool count
 * as well, which no caller read, and a `subtype` whose one branch had a comment for a
 * body.
 */
export function extractResultMetadata(
  parsed: ParsedMessageContent,
  primaryModelId: string | undefined,
): {
  contextWindow?: number
  contextUsage?: ContextUsageInfo
  totalCostUsd?: number
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return null
  // Skip subagent messages — their usage is already included in the parent's totals.
  if (inner.parent_tool_use_id)
    return null

  const result: { contextWindow?: number, contextUsage?: ContextUsageInfo, totalCostUsd?: number } = {}

  if (inner.modelUsage && typeof inner.modelUsage === 'object') {
    const cw = findPrimaryContextWindow(inner.modelUsage as Record<string, unknown>, primaryModelId)
    if (cw > 0)
      result.contextWindow = cw
  }

  const normalizedContextUsage = normalizeContextUsage(inner[SESSION_INFO_KEY.ContextUsage])
  if (normalizedContextUsage)
    result.contextUsage = normalizedContextUsage

  const totalCostUsd = pickNumber(inner, SESSION_INFO_KEY.TotalCostUsd, undefined)
  if (totalCostUsd !== undefined)
    result.totalCostUsd = totalCostUsd

  return Object.keys(result).length > 0 ? result : null
}

/** Extract settings changes from a LEAPMUX settings_changed inner message. */
export function extractSettingsChanges(parsed: ParsedMessageContent): {
  [key: string]: { old: string, new: string } | undefined
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.type !== NOTIFICATION_TYPE.SettingsChanged)
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
      if (m.type === NOTIFICATION_TYPE.PlanUpdated) {
        return {
          planTitle: pickString(m, NOTIFICATION_FIELD.PlanTitle),
          planFilePath: pickString(m, NOTIFICATION_FIELD.PlanFilePath),
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
      if (m.type === NOTIFICATION_TYPE.PlanExecution && pickString(m, NOTIFICATION_FIELD.PlanFilePath) !== '') {
        return pickString(m, NOTIFICATION_FIELD.PlanFilePath)
      }
    }
  }
  return undefined
}

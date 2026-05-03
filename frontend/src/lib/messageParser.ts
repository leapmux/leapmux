import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ContextUsageInfo, RateLimitInfo } from '~/stores/agentSession.store'
import type { TodoItem } from '~/stores/chat.store'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { decompressContentToString } from '~/lib/decompress'
import { isObject, pickFirstNumber, pickNumber } from '~/lib/jsonPick'
import { CODEX_RATE_LIMITS_METHOD, iterCodexRateLimitTiers } from '~/lib/rateLimitUtils'
import { normalizeTodoStatus, rawTodosToItems } from '~/stores/chat.store'

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
// MessageBubble render path, findLatestTodos, etc.). The WeakMap lets
// trimmed/replaced messages get GC'd without manual eviction.
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

// ---------------------------------------------------------------------------
// Domain-specific extractors
// ---------------------------------------------------------------------------

/** Convert todo items to a markdown checklist string. */
export function todosToMarkdown(items: ReadonlyArray<{ status: string, content: string }>): string {
  return items.map((t) => {
    const mark = t.status === 'completed' ? 'x' : t.status === 'in_progress' ? '~' : ' '
    return `- [${mark}] ${t.content}`
  }).join('\n')
}

/** Convert a Codex plan array (from turn/plan/updated) to TodoItem[]. */
export function codexPlanToTodos(plan: unknown[]): TodoItem[] {
  return plan.flatMap((entry) => {
    if (typeof entry !== 'object' || entry === null)
      return []
    const step = String((entry as Record<string, unknown>).step || '')
    if (!step)
      return []
    return [{
      content: step,
      status: normalizeTodoStatus((entry as Record<string, unknown>).status),
      activeForm: step,
    }]
  })
}

/** Extract TodoWrite todos from a parsed assistant message. Returns null if not applicable. */
export function extractTodos(message: AgentChatMessage, parsed: ParsedMessageContent): TodoItem[] | null {
  // Source gate: a USER-side row may legitimately echo back an
  // assistant-shape tool_use envelope (Claude tool_result chunks
  // arrive with role:"user" on the wire). Treating those as todos
  // would surface stale TodoWrite contents on the user side.
  if (message.source !== MessageSource.AGENT)
    return null
  const parent = parsed.parentObject
  if (!parent)
    return null

  if (parent.type === 'assistant') {
    const msg = parent.message as Record<string, unknown> | undefined
    const content = msg?.content
    if (!Array.isArray(content))
      return null
    const toolUse = content.find(
      (c: Record<string, unknown>) => typeof c === 'object' && c !== null && c.type === 'tool_use' && c.name === 'TodoWrite',
    )
    if (!toolUse?.input?.todos || !Array.isArray(toolUse.input.todos))
      return null
    return rawTodosToItems(toolUse.input.todos)
  }

  if (parent.method === 'turn/plan/updated') {
    const params = parent.params as Record<string, unknown> | undefined
    const plan = params?.plan
    if (!Array.isArray(plan))
      return null
    const todos = codexPlanToTodos(plan)
    return todos.length > 0 ? todos : null
  }

  return null
}

/** Scan messages backward for the latest TodoWrite, returning todos or null. */
export function findLatestTodos(messages: AgentChatMessage[]): TodoItem[] | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    const parsed = parseMessageContent(messages[i])
    const todos = extractTodos(messages[i], parsed)
    if (todos)
      return todos
  }
  return null
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

/** Extract context usage from an assistant message's inner usage field. */
export function extractAssistantUsage(parsed: ParsedMessageContent): {
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
  if (normalizedContextUsage)
    result.contextUsage = normalizedContextUsage

  const usage = (inner.message as Record<string, unknown> | undefined)?.usage as
    Record<string, unknown> | undefined
  if (usage && !result.contextUsage) {
    // Claude Code shape.
    if (typeof usage.input_tokens === 'number') {
      result.contextUsage = {
        inputTokens: usage.input_tokens as number,
        cacheCreationInputTokens: pickNumber(usage, 'cache_creation_input_tokens', 0),
        cacheReadInputTokens: pickNumber(usage, 'cache_read_input_tokens', 0),
      }
    }
    // Pi shape, retained as a fallback for raw/unaugmented messages. Newer
    // backend messages carry a normalized top-level contextUsage above.
    else if (typeof usage.input === 'number') {
      const inputTokens = usage.input as number
      const cacheCreationInputTokens = pickNumber(usage, 'cacheWrite', 0)
      const cacheReadInputTokens = pickNumber(usage, 'cacheRead', 0)
      const outputTokens = pickNumber(usage, 'output', undefined)
      const totalTokens = pickNumber(usage, 'totalTokens', undefined)
      const hasPiTokenData = inputTokens > 0
        || cacheCreationInputTokens > 0
        || cacheReadInputTokens > 0
        || (outputTokens ?? 0) > 0
        || (totalTokens ?? 0) > 0
      if (hasPiTokenData) {
        const piUsage: ContextUsageInfo = {
          inputTokens,
          cacheCreationInputTokens,
          cacheReadInputTokens,
        }
        if (outputTokens !== undefined)
          piUsage.outputTokens = outputTokens
        if (totalTokens !== undefined && totalTokens > 0)
          piUsage.contextTokens = totalTokens
        result.contextUsage = piUsage
      }
    }
  }

  return Object.keys(result).length > 0 ? result : null
}

/** Extract context usage from a persisted Codex thread/tokenUsage/updated notification. */
export function extractCodexTokenUsage(parsed: ParsedMessageContent): {
  contextUsage: ContextUsageInfo
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.method !== 'thread/tokenUsage/updated')
    return null

  const params = inner.params as Record<string, unknown> | undefined
  const tokenUsage = params?.tokenUsage as Record<string, unknown> | undefined
  const last = tokenUsage?.last as Record<string, unknown> | undefined
  if (!last || typeof last.inputTokens !== 'number')
    return null

  const contextUsage: ContextUsageInfo = {
    inputTokens: Math.max((last.inputTokens as number) - (typeof last.cachedInputTokens === 'number' ? last.cachedInputTokens as number : 0), 0),
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: typeof last.cachedInputTokens === 'number' ? last.cachedInputTokens as number : 0,
  }
  if (typeof tokenUsage?.modelContextWindow === 'number')
    contextUsage.contextWindow = tokenUsage.modelContextWindow as number

  return { contextUsage }
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

/** Extract result-message metadata: subtype, context usage/window, totalCostUsd, numToolUses. */
export function extractResultMetadata(parsed: ParsedMessageContent, primaryModelId?: string): {
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

  // Codex turn/completed: detect via turn.status and map to the same shape.
  const turn = inner.turn as Record<string, unknown> | undefined
  if (!result.subtype && turn && typeof turn === 'object' && typeof turn.status === 'string')
    result.subtype = 'turn_completed'

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

/** Extract rate limit info from a Claude rate_limit_event or Codex rateLimits/updated inner message. */
export function extractRateLimitInfo(parsed: ParsedMessageContent): {
  key: string
  info: RateLimitInfo
}[] {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return []

  // Claude raw rate_limit_event format: {type:"rate_limit_event", rate_limit_info:{...}}
  if (inner.type === 'rate_limit_event') {
    const rlInfo = inner.rate_limit_info as Record<string, unknown> | undefined
    if (!rlInfo || typeof rlInfo !== 'object')
      return []
    const key = (rlInfo.rateLimitType as string) || 'unknown'
    return [{ key, info: rlInfo as RateLimitInfo }]
  }

  // Codex native format: {method:"account/rateLimits/updated", params:{rateLimits:{primary:{...},secondary:{...}}}}
  if (inner.method === CODEX_RATE_LIMITS_METHOD) {
    const results: { key: string, info: RateLimitInfo }[] = []
    for (const { info } of iterCodexRateLimitTiers(inner)) {
      if (info.rateLimitType)
        results.push({ key: info.rateLimitType, info })
    }
    return results
  }

  return []
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
  const messagesToCheck: unknown[] = parsed.wrapper
    ? parsed.wrapper.messages
    : parsed.topLevel ? [parsed.topLevel] : []
  // Iterate in reverse so the most recent entry in a consolidated thread wins.
  for (let i = messagesToCheck.length - 1; i >= 0; i--) {
    const msg = messagesToCheck[i]
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
  const messagesToCheck: unknown[] = parsed.wrapper
    ? parsed.wrapper.messages
    : parsed.topLevel ? [parsed.topLevel] : []
  for (const msg of messagesToCheck) {
    if (typeof msg === 'object' && msg !== null) {
      const m = msg as Record<string, unknown>
      if (m.type === 'plan_execution' && typeof m.plan_file_path === 'string' && m.plan_file_path !== '') {
        return m.plan_file_path as string
      }
    }
  }
  return undefined
}

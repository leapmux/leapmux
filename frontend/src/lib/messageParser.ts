import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ContextUsageInfo, RateLimitInfo } from '~/stores/agentSession.store'
import type { TodoItem } from '~/stores/chat.store'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { decompressContentToString } from '~/lib/decompress'
import { codexTierToRateLimitInfo } from '~/lib/rateLimitUtils'

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

/**
 * Decompress and parse an AgentChatMessage's content in a single pass.
 * Never throws -- returns safe defaults on any failure.
 *
 * Notification-threaded messages use a wrapper format:
 *   {"old_seqs": [...], "messages": [{...}, ...]}
 * Both LEAPMUX and SYSTEM role messages may use this format.
 * All other messages are stored as raw JSON (no wrapper).
 */
export function parseMessageContent(message: AgentChatMessage): ParsedMessageContent {
  const text = decompressContentToString(message.content, message.contentCompression)
  if (text === null)
    return EMPTY_PARSED

  try {
    const obj = JSON.parse(text)

    // Notification-threaded messages use the wrapper format for consolidation.
    // Both LEAPMUX and SYSTEM role messages may use this format (e.g. api_retry,
    // compact_boundary, microcompact_boundary are stored with SYSTEM role).
    if ((message.role === MessageRole.LEAPMUX || message.role === MessageRole.SYSTEM) && obj?.messages && Array.isArray(obj.messages)) {
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
    const status = (entry as Record<string, unknown>).status
    return [{
      content: step,
      status: status === 'inProgress' ? 'in_progress' as const : status === 'completed' ? 'completed' as const : 'pending' as const,
      activeForm: step,
    }]
  })
}

/** Extract TodoWrite todos from a parsed assistant message. Returns null if not applicable. */
export function extractTodos(message: AgentChatMessage, parsed: ParsedMessageContent): TodoItem[] | null {
  if (message.role !== MessageRole.ASSISTANT)
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
    return toolUse.input.todos.map((t: Record<string, unknown>) => ({
      content: String(t.content || ''),
      status: t.status === 'in_progress' ? 'in_progress' as const : t.status === 'completed' ? 'completed' as const : 'pending' as const,
      activeForm: String(t.activeForm || ''),
    }))
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
  const usage = (inner.message as Record<string, unknown> | undefined)?.usage as
    Record<string, unknown> | undefined
  if (!usage)
    return null

  const result: { totalCostUsd?: number, contextUsage?: ContextUsageInfo } = {}
  if (typeof inner.total_cost_usd === 'number')
    result.totalCostUsd = inner.total_cost_usd as number
  if (typeof usage.input_tokens === 'number') {
    result.contextUsage = {
      inputTokens: (usage.input_tokens as number) ?? 0,
      cacheCreationInputTokens: (usage.cache_creation_input_tokens as number) ?? 0,
      cacheReadInputTokens: (usage.cache_read_input_tokens as number) ?? 0,
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

/** Extract result-message metadata: subtype, contextWindow, totalCostUsd, numToolUses. */
export function extractResultMetadata(parsed: ParsedMessageContent): {
  subtype?: string
  contextWindow?: number
  totalCostUsd?: number
  numToolUses?: number
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return null
  // Skip subagent messages — their usage is already included in the parent's totals.
  if (inner.parent_tool_use_id)
    return null

  const result: { subtype?: string, contextWindow?: number, totalCostUsd?: number, numToolUses?: number } = {}

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
    for (const modelData of Object.values(inner.modelUsage as Record<string, unknown>)) {
      const md = modelData as Record<string, unknown> | undefined
      if (md && typeof md.contextWindow === 'number') {
        result.contextWindow = md.contextWindow as number
        break
      }
    }
  }

  if (typeof inner.total_cost_usd === 'number')
    result.totalCostUsd = inner.total_cost_usd as number

  return Object.keys(result).length > 0 ? result : null
}

/** Extract rate limit info from a LEAPMUX rate_limit or Codex rateLimits/updated inner message. */
export function extractRateLimitInfo(parsed: ParsedMessageContent): {
  key: string
  info: RateLimitInfo
}[] {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return []

  // Claude Code format: {type: "rate_limit", rate_limit_info: {...}}
  if (inner.type === 'rate_limit') {
    const rlInfo = inner.rate_limit_info as Record<string, unknown> | undefined
    if (!rlInfo || typeof rlInfo !== 'object')
      return []
    const key = (rlInfo.rateLimitType as string) || 'unknown'
    return [{ key, info: rlInfo as RateLimitInfo }]
  }

  // Codex native format: {method: "account/rateLimits/updated", params: {rateLimits: {primary: {...}, secondary: {...}}}}
  if (inner.method === 'account/rateLimits/updated') {
    const params = inner.params as Record<string, unknown> | undefined
    const rl = params?.rateLimits as Record<string, unknown> | undefined
    if (!rl)
      return []
    const results: { key: string, info: RateLimitInfo }[] = []
    for (const tierKey of ['primary', 'secondary']) {
      const tier = rl[tierKey] as Record<string, unknown> | undefined
      if (!tier)
        continue
      const info = codexTierToRateLimitInfo(tier)
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

/** Extract renamed title from an agent_renamed notification (wrapped or unwrapped). */
export function extractAgentRenamed(parsed: ParsedMessageContent): string | undefined {
  const messagesToCheck: unknown[] = parsed.wrapper
    ? parsed.wrapper.messages
    : parsed.topLevel ? [parsed.topLevel] : []
  for (const msg of messagesToCheck) {
    if (typeof msg === 'object' && msg !== null) {
      const m = msg as Record<string, unknown>
      if (m.type === 'agent_renamed' && typeof m.title === 'string' && m.title !== '') {
        return m.title as string
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

import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ContextUsageInfo } from '~/stores/agentSession.store'
import type { TodoItem } from '~/stores/chat.store'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { decompressContentToString } from '~/lib/decompress'

/**
 * The result of parsing a compressed AgentChatMessage. Every field is
 * derived from a single decompress-then-JSON.parse pass.
 */
export interface ParsedMessageContent {
  /** The raw decompressed text (for "Copy Raw JSON"). */
  rawText: string
  /** The top-level parsed JSON object, or null on parse failure. */
  topLevel: Record<string, unknown> | null
  /** Whether topLevel is a thread-wrapper envelope ({messages: [...]}) */
  isWrapped: boolean
  /** The first (parent) inner message object, or undefined. */
  parentObject: Record<string, unknown> | undefined
  /** Thread children (messages after the first), empty array if none. */
  children: unknown[]
  /** The wrapper envelope if wrapped, null otherwise. */
  wrapper: { old_seqs: number[], messages: unknown[] } | null
}

const EMPTY_PARSED: ParsedMessageContent = {
  rawText: '',
  topLevel: null,
  isWrapped: false,
  parentObject: undefined,
  children: [],
  wrapper: null,
}

/**
 * Decompress and parse an AgentChatMessage's content in a single pass.
 * Never throws -- returns safe defaults on any failure.
 */
export function parseMessageContent(message: AgentChatMessage): ParsedMessageContent {
  const text = decompressContentToString(message.content, message.contentCompression)
  if (text === null)
    return EMPTY_PARSED

  try {
    const obj = JSON.parse(text)
    if (obj?.messages && Array.isArray(obj.messages)) {
      // Wrapped format: {"old_seqs": [...], "messages": [{...}, ...]}
      // An empty messages array can occur when notification consolidation
      // cancels out all changes (e.g. plan mode toggled back).
      if (obj.messages.length === 0)
        return { rawText: text, topLevel: obj, isWrapped: true, parentObject: undefined, children: [], wrapper: obj }

      const first = obj.messages[0]
      const parent = (typeof first === 'object' && first !== null && !Array.isArray(first))
        ? first as Record<string, unknown>
        : undefined
      return {
        rawText: text,
        topLevel: obj,
        isWrapped: true,
        parentObject: parent,
        children: obj.messages.length > 1 ? obj.messages.slice(1) : [],
        wrapper: obj,
      }
    }
    // Not wrapped — treat the parsed object as the parent directly.
    const parent = (typeof obj === 'object' && obj !== null && !Array.isArray(obj))
      ? obj as Record<string, unknown>
      : undefined
    return { rawText: text, topLevel: obj, isWrapped: false, parentObject: parent, children: [], wrapper: null }
  }
  catch {
    return { rawText: text, topLevel: null, isWrapped: false, parentObject: undefined, children: [], wrapper: null }
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

/** Extract TodoWrite todos from a parsed assistant message. Returns null if not applicable. */
export function extractTodos(message: AgentChatMessage, parsed: ParsedMessageContent): TodoItem[] | null {
  if (message.role !== MessageRole.ASSISTANT)
    return null
  const parent = parsed.parentObject
  if (!parent || parent.type !== 'assistant')
    return null
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

/** Extract result-message metadata: subtype, contextWindow, totalCostUsd. */
export function extractResultMetadata(parsed: ParsedMessageContent): {
  subtype?: string
  contextWindow?: number
  totalCostUsd?: number
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return null

  const result: { subtype?: string, contextWindow?: number, totalCostUsd?: number } = {}

  if (inner.subtype)
    result.subtype = inner.subtype as string

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

/** Extract rate limit info from a LEAPMUX rate_limit inner message. */
export function extractRateLimitInfo(parsed: ParsedMessageContent): {
  key: string
  info: Record<string, unknown>
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.type !== 'rate_limit')
    return null
  const rlInfo = inner.rate_limit_info as Record<string, unknown> | undefined
  if (!rlInfo || typeof rlInfo !== 'object')
    return null
  const key = (rlInfo.rateLimitType as string) || 'unknown'
  return { key, info: rlInfo }
}

/** Extract settings changes from a LEAPMUX settings_changed inner message. */
export function extractSettingsChanges(parsed: ParsedMessageContent): {
  permissionMode?: { old: string, new: string }
} | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.type !== 'settings_changed')
    return null
  const changes = inner.changes as Record<string, unknown> | undefined
  if (!changes || typeof changes !== 'object')
    return null
  return changes as { permissionMode?: { old: string, new: string } }
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

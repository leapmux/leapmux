// ---------------------------------------------------------------------------
// The two readings a session reports about itself: how much of the context
// window it holds, and where it stands against the provider's rate limits.
//
// Domain models, not store state. Ten provider extractors produce them. The chat model carries
// them inside a notification, `lib/rateLimitUtils` derives words from them, and
// `ContextUsageGrid` draws them -- so the store is one consumer among many. They
// lived in `stores/agentSession.store.ts`, which made a provider extractor and
// a chat-model module import from the store layer for a type neither layer owns.
// ---------------------------------------------------------------------------

export interface ContextUsageInfo {
  inputTokens: number
  cacheCreationInputTokens: number
  cacheReadInputTokens: number
  outputTokens?: number
  /** Authoritative provider-reported current context size, when available. */
  contextTokens?: number
  contextWindow?: number
}

export interface RateLimitInfo {
  status?: string // "allowed" | "allowed_warning" | "exceeded" etc.
  resetsAt?: number // Unix timestamp (seconds)
  rateLimitType?: string // "five_hour" | "seven_day" etc.
  utilization?: number // 0.0–1.0, current usage fraction
  surpassedThreshold?: number // threshold that triggered warning (e.g. 0.75)
  overageStatus?: string // "allowed" etc.
  overageResetsAt?: number // Unix timestamp (seconds)
  isUsingOverage?: boolean
}

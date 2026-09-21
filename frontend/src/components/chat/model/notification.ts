import type { RateLimitInfo } from '~/models/agentSession'

/**
 * The glyph a notification divider asks for, stated as what it MEANS.
 *
 * A closed set, so the renderer's map over it is exhaustive and a new outcome
 * cannot reach the screen with no glyph. The model names the outcome rather than
 * the icon component: the icon set is a rendering decision, and a swap of one
 * glyph for another must not edit the layer that reads the provider's bytes.
 *
 * A divider that asks for none takes the renderer's default.
 */
export type NotificationIconHint = 'succeeded' | 'failed' | 'stopped' | 'interrupted'

/** One settings axis that changed, already resolved to display words. */
export interface SettingChange {
  label: string
  /** The prior value, or undefined when the axis had none. */
  old?: string
  new: string
}

/** What a compaction boundary states about the context it rewrote. */
export interface CompactionDetails {
  /** What asked for the compaction: `manual`, `auto`, a provider's own word. */
  trigger?: string
  /** Tokens before the compaction. */
  pre?: number
  /** Tokens after it. */
  post?: number
}

/**
 * One entry of a notification thread, after the provider read one message.
 *
 * The shared renderer draws these; nothing below it knows a provider. `text` is the
 * escape hatch for a statement that needs no structure. Every other variant exists
 * because the FORMATTING was worth sharing: three providers formatted a rate-limit
 * tier separately, and two of them rounded the percentage differently.
 */
export type NotificationEntry
  = | { kind: 'text', text: string }
    | { kind: 'subagent-report', label?: string, text: string, status?: string }
  /**
   * A statement that COALESCES with its neighbours under one prefix. A run of
   * entries with the same `groupKey` collapses into `Prefix: a, b, c`.
   */
    | { kind: 'group', groupKey: string, prefix: string, entry: string }
    /**
     * A full-width labelled rule, drawn in the same style as a turn-end divider.
     * `loading` swaps the glyph for a spinner; `icon` overrides the default
     * compaction arrow (a subagent-end divider states one outcome per glyph).
     */
    | { kind: 'divider', text: string, loading?: boolean, icon?: NotificationIconHint }
    /**
     * One entry per rate-limit window the provider reported.
     *
     * The tiers are the store's own `RateLimitInfo`, not a second shape: Claude's
     * `rate_limit_event` and Codex's `account/rateLimits/updated` already normalize
     * into it for the usage meter, and `formatRateLimitMessage` already writes the
     * sentence from it. A parallel type here would be a second rounding of the same
     * percentage.
     */
    | { kind: 'rate-limit', tiers: RateLimitInfo[] }
    | { kind: 'settings-changed', changes: SettingChange[] }
    /**
     * The agent waits before it tries again. `scope` says WHAT it retries: the model
     * call itself, or the summarization that compaction runs.
     */
    | {
      kind: 'retry'
      scope: 'api' | 'summarization'
      attempt?: number
      maxAttempts?: number
      /** How long the agent waits before the next attempt. */
      delayMs?: number
      error?: string
      /** False states that the agent gave up, which reads differently from a wait. */
      willRetry?: boolean
      /** True states that the retry WORKED, which ends the stall the reader watched. */
      succeeded?: boolean
    }
    /**
     * The agent rewrote its own context.
     *
     * `micro` marks Claude Code's MICROcompaction, which is a different event from a
     * full one: it rewrites a fraction of the context, it carries no metadata at all,
     * and the row must not claim the full compaction happened.
     */
    | { kind: 'compaction', phase: CompactionPhase, micro?: boolean, detail?: CompactionDetails, error?: string }
    | { kind: 'context-cleared' }
    /** A transient progress notice: Goose's `status_message`, Reasonix's phase changes. */
    | { kind: 'status', text: string }

/** Which end of a context rewrite one compaction row states. */
export type CompactionPhase = 'start' | 'end'

/** Every entry one notification row draws, in order. */
export interface NotificationThread {
  entries: NotificationEntry[]
}

// The compaction-metadata keys, which every provider spells one of two ways.
// Internal: callers resolve metadata through `compactionMetaFromRecord`.
const COMPACT_META_KEYS = ['compact_metadata', 'compactMetadata'] as const

/**
 * Coerce a raw numeric token count to a usable value: finite and non-negative.
 *
 * A non-finite input (NaN/Infinity -- which JSON cannot carry but a synthesized
 * payload could) degrades to undefined. A negative clamps to 0, so a provider that
 * reports an explicit negative count, or a derived `pre - saved` where saved exceeds
 * pre, yields 0 rather than a negative size.
 */
export function toTokenCount(n: number | undefined): number | undefined {
  if (n === undefined || !Number.isFinite(n))
    return undefined
  return Math.max(0, n)
}

function firstNumber(source: Record<string, unknown> | undefined, keys: readonly string[]): number | undefined {
  for (const key of keys) {
    const value = source?.[key]
    if (typeof value === 'number')
      return value
  }
  return undefined
}

/**
 * Resolve the post-compaction token count from raw metadata.
 *
 * `post_tokens` wins (Claude's `compact_boundary` carries it directly). When only a
 * `tokens_saved` delta is present beside `pre`, post derives as `pre - saved`, which
 * {@link toTokenCount} later clamps to at least 0.
 */
function resolvePostTokens(meta: Record<string, unknown> | undefined, pre: number | undefined): number | undefined {
  const post = firstNumber(meta, ['post_tokens', 'postTokens'])
  if (typeof post === 'number')
    return post
  const saved = firstNumber(meta, ['tokens_saved', 'tokensSaved'])
  if (typeof pre === 'number' && typeof saved === 'number')
    return pre - saved
  return undefined
}

/**
 * Read a raw compaction-metadata object into the neutral boundary shape.
 *
 * The keys, not the boundary: WHICH frame is a boundary is each provider's own
 * question, and its `compactionBoundaryFromMessage` hook answers it. What every
 * provider agrees on is that the metadata arrives under one of two spellings and
 * carries the same three facts, so that reading lives here once.
 */
export function compactionMetaFromRecord(meta: Record<string, unknown> | undefined): CompactionDetails {
  // Each fact rides only when the metadata stated it; an unstated one stays absent
  // rather than arriving as an explicitly undefined key.
  const rawPre = firstNumber(meta, ['pre_tokens', 'preTokens'])
  const pre = toTokenCount(rawPre)
  const post = toTokenCount(resolvePostTokens(meta, rawPre))
  const trigger = typeof meta?.trigger === 'string' && meta.trigger ? meta.trigger : undefined
  return {
    ...(trigger !== undefined ? { trigger } : {}),
    ...(pre !== undefined ? { pre } : {}),
    ...(post !== undefined ? { post } : {}),
  }
}

/** Read the metadata of a boundary that carries it under one of the two standard keys. */
export function compactionMetaFromBoundary(m: Record<string, unknown>): CompactionDetails {
  for (const key of COMPACT_META_KEYS) {
    const value = m[key]
    if (value !== null && typeof value === 'object')
      return compactionMetaFromRecord(value as Record<string, unknown>)
  }
  return compactionMetaFromRecord(undefined)
}

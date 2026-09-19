// Provider registry. Ten plugin modules call registerProvider() at import time as a
// side effect -- five of them directly, and five through registerACPProvider. The
// side-effect imports live in providers/index.ts. providerFor() returns undefined for
// a provider nobody imported, so a caller that needs a registered provider imports
// providers/index.ts, or that provider's own module, first.
//
// This mirrors the backend's `agent.Provider` interface and `agent.ProviderFor`
// lookup; each side carries the per-provider hooks its layer needs. The plugin's
// SHAPE is the four capabilities of `capabilities.ts`; this module owns registration
// and lookup alone.

import type { ToolRowOutcome } from '../ir/toolOutcomeLabel'
import type { ResolvedMessageContent } from '../rowExtractionTypes'
import type { ProviderPlugin } from './capabilities'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { messageCompletionFromProto } from '../assembledMessage'
import { applyMessageMetadata } from '../messageMetadata'

// The types a plugin's importers name. They live in capabilities.ts beside the
// capability interfaces that use them; the registry stays the module every caller
// already imports, so a reader asks for a plugin and its types in one place.
export type {
  AttachmentCapabilities,
  ClassificationContext,
  ClassificationInput,
  ControlExtractionInput,
  ExtractedControlRequest,
  ProviderAskUserQuestion,
  ProviderConfigurationCapability,
  ProviderControlCapability,
  ProviderPlugin,
  ProviderSessionCapability,
  ProviderTranscriptCapability,
} from './capabilities'

/**
 * How LeapMux itself says a retained tool row ended, or null when it says nothing.
 *
 * A turn that ends while a tool call runs leaves no final frame, so the worker stores
 * the agent's own LAST frame and states the outcome in its completion column instead.
 * That frame still reads as pending, running or started, so a renderer that draws a
 * status from the provider bytes alone shows a finished call as a running one.
 *
 * The rule is LeapMux's, not any provider's, so it lives HERE and each caller maps the
 * one word into its own vocabulary: an ACP `cancelled`/`failed` tool status, the MCP
 * card's `failed`, the command card's `interrupted` and `isError` booleans. Four sites
 * spelled the test separately and gave three different answers for the same row.
 *
 * Null means that the worker recorded no completion. The provider's own bytes then
 * state the outcome, and a caller keeps whatever they say.
 */
export function retainedOutcome(completion: MessageCompletion | undefined): ToolRowOutcome | null {
  switch (messageCompletionFromProto(completion)) {
    case 'complete':
      return 'succeeded'
    case 'interrupted':
      return 'interrupted'
    case 'error':
      return 'failed'
    default:
      return null
  }
}

/**
 * True when this row is the final row of its tool span whatever its provider bytes
 * say.
 *
 * A span role read from the provider status alone calls the closing row an opener, for
 * the reason {@link retainedOutcome} gives. Every `spanRole` hook reads the rule from
 * here rather than spelling the completion test again.
 */
export function retainedRowIsFinal(completion: MessageCompletion | undefined): boolean {
  return retainedOutcome(completion) !== null
}

const registry = new Map<AgentProvider, ProviderPlugin>()

/**
 * Register one provider's plugin.
 *
 * `UNSPECIFIED` is refused: it is the proto's "no provider stated" value, and a plugin
 * behind it would answer for messages that state no provider at all. A SECOND
 * registration of a provider already registered is refused the same way -- the
 * last-write-wins it silently performed left the registry holding whichever plugin
 * imported later, which is a bundling decision rather than a program decision. A test
 * that needs to replace a plugin resets the registry first
 * ({@link __resetProviderRegistryForTest}).
 */
export function registerProvider(provider: AgentProvider, plugin: ProviderPlugin): void {
  if (provider === AgentProvider.UNSPECIFIED)
    throw new Error('registerProvider: UNSPECIFIED names no provider')
  if (registry.has(provider))
    throw new Error(`registerProvider: ${AgentProvider[provider]} is already registered`)
  registry.set(provider, plugin)
}

/** Test-only: drop every registration, so a test can register its own stubs. */
export function __resetProviderRegistryForTest(): void {
  registry.clear()
}

export function providerFor(provider: AgentProvider): ProviderPlugin | undefined {
  return registry.get(provider)
}

/**
 * Per-provider seed option selections for a fresh agent (e.g. Codex's collaboration
 * mode), shaped to spread directly into an OpenAgent request:
 * `...openAgentRequestOptions(provider)`. The plugin owns what (if anything) to seed via
 * its `defaultProviderOptions`; the worker fills every other axis with its provider
 * defaults. Returns `{}` when the provider seeds nothing, so the request omits `options`
 * rather than sending an empty map. Centralizing this keeps a new provider's seeding from
 * being wired into some agent-open paths but not others.
 */
export function openAgentRequestOptions(provider: AgentProvider): { options?: Record<string, string> } {
  const options = providerFor(provider)?.configuration?.defaultProviderOptions
  return options ? { options } : {}
}

/**
 * Resolve a message/agent's own provider plugin, with no Claude (or any other)
 * fallback. A nullish provider (an absent `agentProvider` field) and an
 * unregistered enum value both yield `undefined`: callers must treat a
 * missing plugin as a misconfiguration to surface (e.g. `unsupported_provider`)
 * rather than guessing another provider's renderers for this one's bytes. This
 * is the single chokepoint for that "dispatch strictly by provider" rule, so
 * the no-guessing contract lives in one place instead of a ternary at every
 * call site.
 */
/**
 * The span role of one RESOLVED parse, through its provider's hook.
 *
 * The one route the four shared readers take: it resolves the parse, asks the
 * plugin, and answers `other` when no provider exists -- so no production call
 * site hands a provider's `spanRole` a raw parse, and a plugin that registers
 * late is read through the same memo as every other hook.
 */
export function resolvedSpanRole(parsed: ParsedMessageContent, provider: AgentProvider): ToolSpanRole {
  return pluginFor(provider)?.transcript.spanRole?.(resolveMessageForRendering(parsed, provider)) ?? 'other'
}

export function pluginFor(provider: AgentProvider | undefined): ProviderPlugin | undefined {
  return provider != null ? providerFor(provider) : undefined
}

/**
 * The resolved-parse memo: one parse, one entry per provider.
 *
 * `WeakMap` keyed on the parse, because the parse is cached on the message
 * reference -- the same object reaches this function for every reader of one row,
 * and resolving it twice built a second object for the same bytes, which put the
 * transcript, the toolbar and the image tab on different copies. The resolved
 * object is stored as its OWN cached result too, so a second resolution of an
 * already-resolved parse returns the same reference.
 *
 * Replaced wholesale when provider registration changes or a test resets the
 * registry: a plugin that registered later must not answer from a memo the
 * previous registration wrote.
 */
let resolvedMemo = new WeakMap<ParsedMessageContent, Map<AgentProvider, ResolvedMessageContent>>()

/** Drop the resolved-parse memo. Test-only: the registry reset must invalidate it. */
export function __resetResolvedMessageMemoForTest(): void {
  resolvedMemo = new WeakMap()
}

/**
 * Resolve display data without changing the parsed original used by the Raw JSON view.
 *
 * The ONE constructor of {@link ResolvedMessageContent}: the merge runs once per
 * parse and provider, and the brand it returns is what the classifiers, the
 * span-role readers and the extractors accept -- never the raw parse.
 */
export function resolveMessageForRendering(parsed: ParsedMessageContent, provider: AgentProvider): ResolvedMessageContent {
  let byProvider = resolvedMemo.get(parsed)
  if (byProvider === undefined) {
    byProvider = new Map()
    resolvedMemo.set(parsed, byProvider)
  }
  const cached = byProvider.get(provider)
  if (cached !== undefined)
    return cached
  const providerData = providerFor(provider)?.transcript.resolveMessage?.(parsed) ?? parsed.parentObject
  const parentObject = providerData && isObject(parsed.messageMetadata) ? applyMessageMetadata(providerData, parsed.messageMetadata) : providerData
  const resolved = (parentObject === parsed.parentObject ? parsed : { ...parsed, parentObject }) as ResolvedMessageContent
  byProvider.set(provider, resolved)
  // The resolved object answers for itself: a second resolution of it -- a caller
  // that held the merged parse and passed it back -- returns the same reference
  // rather than running the provider hook over its own output.
  let selfEntry = resolvedMemo.get(resolved)
  if (selfEntry === undefined) {
    selfEntry = new Map()
    resolvedMemo.set(resolved, selfEntry)
  }
  if (!selfEntry.has(provider))
    selfEntry.set(provider, resolved)
  return resolved
}

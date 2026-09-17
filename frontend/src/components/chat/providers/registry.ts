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
import type { ProviderPlugin } from './capabilities'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
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
export type { SpanRole } from '~/components/chat/rowExtractionTypes'

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
export function pluginFor(provider: AgentProvider | undefined): ProviderPlugin | undefined {
  return provider != null ? providerFor(provider) : undefined
}

/** Resolve display data without changing the parsed original used by the Raw JSON view. */
export function parsedMessageForRendering(parsed: ParsedMessageContent, provider: AgentProvider): ParsedMessageContent {
  const providerData = providerFor(provider)?.transcript.resolveMessage?.(parsed) ?? parsed.parentObject
  const parentObject = providerData && isObject(parsed.messageMetadata) ? applyMessageMetadata(providerData, parsed.messageMetadata) : providerData
  return parentObject === parsed.parentObject ? parsed : { ...parsed, parentObject }
}

import type { MessageCategory } from './messageClassification'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pluginFor } from './providers/registry'

/**
 * Per-message UI state keys consumed via `getMessageUiState`/`setMessageUiState`
 * (or `useSharedExpandedState`). Centralized so renderers can't collide on a
 * hand-typed string and so adding a new flag has one obvious home.
 *
 * A key that ONE provider draws under (the three Codex ones) still belongs here. This
 * table is the key vocabulary, not the routing: WHICH key a row takes is the provider's
 * own decision, and `Provider.expandUiKey` holds it. `MESSAGE_UI_DEFAULTS` below is a
 * `Record<MessageUiKey, ...>`, so a key a plugin registered for itself would forfeit
 * the missing-default compile error.
 */
export const MESSAGE_UI_KEY = {
  TOOL_RESULT_EXPANDED: 'tool-result-expanded',
  TOOL_USE_LAYOUT: 'tool-use-layout',
  AGENT_PROMPT: 'agent-prompt',
  THINKING: 'thinking',
  PLAN_EXECUTION: 'plan-execution',
  CODEX_COMMAND_EXECUTION: 'codex-command-execution',
  CODEX_WEB_SEARCH: 'codex-web-search',
  CODEX_REASONING: 'codex-reasoning',
  UNRECOGNIZED_ROW: 'unrecognized-row',
} as const

export type MessageUiKey = typeof MESSAGE_UI_KEY[keyof typeof MESSAGE_UI_KEY]

/** Inputs a per-message UI key's default may depend on (only the global pref so far). */
export interface MessageUiDefaultContext {
  /** The global "expand agent thoughts" preference, when known. */
  expandAgentThoughts?: boolean
}

/**
 * The default expanded/active value for each per-message UI key when no explicit
 * per-message override has been set. The SINGLE source of truth shared by the
 * renderers (via `useSharedExpandedState` / `getToolResultExpanded`) and
 * ChatView's row-state resolver, so a default can never drift between visible
 * render and hidden premeasure render.
 *
 * Thinking/reasoning bubbles follow the global `expandAgentThoughts` pref;
 * everything else defaults collapsed. A renderer with a genuinely per-row default
 * (e.g. Codex MCP tool calls expand when terminal) still passes its own `initial`
 * to `useSharedExpandedState`, which overrides this table entry. Keyed by every
 * `MessageUiKey`, so adding a key forces a default here (a missing entry fails to
 * compile) rather than silently defaulting to `false` at a scattered call site.
 */
export const MESSAGE_UI_DEFAULTS: Record<MessageUiKey, (ctx: MessageUiDefaultContext) => boolean> = {
  [MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED]: () => false,
  [MESSAGE_UI_KEY.TOOL_USE_LAYOUT]: () => false,
  [MESSAGE_UI_KEY.AGENT_PROMPT]: () => false,
  [MESSAGE_UI_KEY.THINKING]: ctx => ctx.expandAgentThoughts ?? true,
  [MESSAGE_UI_KEY.PLAN_EXECUTION]: () => false,
  [MESSAGE_UI_KEY.CODEX_COMMAND_EXECUTION]: () => false,
  [MESSAGE_UI_KEY.CODEX_WEB_SEARCH]: () => false,
  [MESSAGE_UI_KEY.CODEX_REASONING]: ctx => ctx.expandAgentThoughts ?? true,
  // A frame no renderer claimed is nearly always one the transcript has no use for.
  [MESSAGE_UI_KEY.UNRECOGNIZED_ROW]: () => false,
}

/** Resolve a per-message UI key's default expanded value (see MESSAGE_UI_DEFAULTS). */
export function messageUiDefault(key: MessageUiKey, ctx: MessageUiDefaultContext = {}): boolean {
  return MESSAGE_UI_DEFAULTS[key](ctx)
}

/**
 * The per-message UI key for a row's EXPAND toggle (the thinking/reasoning/plan/
 * agent-prompt bubble), resolved from the row's classification kind + provider.
 * The SINGLE source of this mapping: ChatView and the renderers
 * (ThinkingBubble / AgentPromptView, via `RenderContext.expandUiKey`) both read it,
 * so hidden premeasure and visible render cannot assume different keys.
 *
 * `plan_execution` and `agent_prompt` own their keys whatever the provider, so the two
 * kinds answer first. Every other kind asks the provider's own `Provider.expandUiKey`
 * hook, because a bubble that a plugin draws reads a key that the plugin picks -- Codex
 * reasoning is one.
 *
 * Returns THINKING for any other kind: the value is only consumed for the
 * expand-bubble rows above, so a non-thinking row's key is never read -- THINKING is
 * just a harmless default, matching the prior `expandedStateFor` fall-through.
 */
export function expandedUiKeyFor(kind: MessageCategory['kind'], provider: AgentProvider | undefined): MessageUiKey {
  if (kind === 'plan_execution')
    return MESSAGE_UI_KEY.PLAN_EXECUTION
  if (kind === 'agent_prompt')
    return MESSAGE_UI_KEY.AGENT_PROMPT
  return pluginFor(provider)?.expandUiKey?.(kind) ?? MESSAGE_UI_KEY.THINKING
}

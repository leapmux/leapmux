import type { MessageCategory } from './messageClassifier'

/**
 * Per-message UI state keys consumed via `getMessageUiState`/`setMessageUiState`
 * (or `useSharedExpandedState`). Centralized so renderers can't collide on a
 * hand-typed string and so adding a new flag has one obvious home.
 *
 * Every key is KIND-scoped and provider-NEUTRAL. Three keys used to belong to Codex
 * alone, because Codex drew its own reasoning, command and web-search bubbles; those
 * rows draw through the shared components now, so the shared keys serve them and a
 * provider no longer picks a key of its own.
 */
export const MESSAGE_UI_KEY = {
  TOOL_RESULT_EXPANDED: 'tool-result-expanded',
  TOOL_USE_LAYOUT: 'tool-use-layout',
  AGENT_PROMPT: 'agent-prompt',
  THINKING: 'thinking',
  PLAN_EXECUTION: 'plan-execution',
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
 * per-message override exists. The SINGLE source of truth shared by the
 * renderers (via `useSharedExpandedState` / `getToolResultExpanded`) and
 * ChatView's row-state resolver, so a default can never drift between visible
 * render and hidden premeasure render.
 *
 * Thinking/reasoning bubbles follow the global `expandAgentThoughts` pref;
 * everything else defaults collapsed. A renderer with a genuinely per-row default
 * (e.g. Codex MCP tool calls expand when final) still passes its own `initial`
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
  // A frame no renderer claimed is nearly always one the transcript has no use for.
  [MESSAGE_UI_KEY.UNRECOGNIZED_ROW]: () => false,
}

/** Resolve a per-message UI key's default expanded value (see MESSAGE_UI_DEFAULTS). */
export function messageUiDefault(key: MessageUiKey, ctx: MessageUiDefaultContext = {}): boolean {
  return MESSAGE_UI_DEFAULTS[key](ctx)
}

/**
 * The per-message UI key for a row's EXPAND toggle (the thinking/plan/agent-prompt
 * bubble), resolved from the row's classification kind.
 *
 * The SINGLE source of this mapping: ChatView and the renderers (ThinkingBubble /
 * AgentPromptView, via the `expandUiKey` context capability) both read it, so hidden premeasure
 * and visible render cannot assume different keys.
 *
 * It takes NO provider. Each kind draws through one shared component now, so the key a
 * row takes follows from its kind alone -- where a provider used to pick its own and
 * the estimator had to ask which.
 *
 * Returns THINKING for any other kind: the value is only consumed for the
 * expand-bubble rows above, so a non-thinking row's key is never read.
 */
export function expandedUiKeyFor(kind: MessageCategory['kind']): MessageUiKey {
  if (kind === 'plan_execution')
    return MESSAGE_UI_KEY.PLAN_EXECUTION
  if (kind === 'agent_prompt')
    return MESSAGE_UI_KEY.AGENT_PROMPT
  return MESSAGE_UI_KEY.THINKING
}

import type { MessageCategory } from './messageClassification'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'

/**
 * Per-message UI state keys consumed via `getMessageUiState`/`setMessageUiState`
 * (or `useSharedExpandedState`). Centralized so renderers can't collide on a
 * hand-typed string and so adding a new flag has one obvious home.
 */
export const MESSAGE_UI_KEY = {
  TOOL_RESULT_EXPANDED: 'tool-result-expanded',
  TOOL_USE_LAYOUT: 'tool-use-layout',
  AGENT_PROMPT: 'agent-prompt',
  THINKING: 'thinking',
  PLAN_EXECUTION: 'plan-execution',
  CODEX_MCP_TOOL_CALL: 'codex-mcp-tool-call',
  CODEX_COMMAND_EXECUTION: 'codex-command-execution',
  CODEX_WEB_SEARCH: 'codex-web-search',
  CODEX_COLLAB_AGENT_TOOL_CALL: 'codex-collab-agent-tool-call',
  CODEX_REASONING: 'codex-reasoning',
  OPENCODE_TOOL_CALL_UPDATE: 'opencode-tool-call-update',
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
 * renderers (via `useSharedExpandedState` / `getToolResultExpanded`) and the
 * height estimator (ChatView's `rowUiState`/`expandedStateFor`), so a default can
 * never drift between what mounts and what the off-screen estimate assumes.
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
  [MESSAGE_UI_KEY.CODEX_MCP_TOOL_CALL]: () => false,
  [MESSAGE_UI_KEY.CODEX_COMMAND_EXECUTION]: () => false,
  [MESSAGE_UI_KEY.CODEX_WEB_SEARCH]: () => false,
  [MESSAGE_UI_KEY.CODEX_COLLAB_AGENT_TOOL_CALL]: () => false,
  [MESSAGE_UI_KEY.CODEX_REASONING]: ctx => ctx.expandAgentThoughts ?? true,
  [MESSAGE_UI_KEY.OPENCODE_TOOL_CALL_UPDATE]: () => false,
}

/** Resolve a per-message UI key's default expanded value (see MESSAGE_UI_DEFAULTS). */
export function messageUiDefault(key: MessageUiKey, ctx: MessageUiDefaultContext = {}): boolean {
  return MESSAGE_UI_DEFAULTS[key](ctx)
}

/**
 * Which interactive UI flags a row's CONTENT BODY actually responds to, by
 * classification kind (+ tool name). The SINGLE source consulted by BOTH the height
 * estimator's feature extractor (`chatHeightInput.buildGenericInput`, which applies
 * a flag to the analytical height) AND the UI-state resolver (ChatView's
 * `rowUiState`, which resolves the live value). Centralizing it means a new
 * collapsible/expandable kind declares its flag in ONE place -- it can't resolve a
 * flag the estimator never reads (or vice-versa), the off-screen-estimate-ignores-a-
 * toggle drift this guards against.
 *
 * NOT included: `diffView`. Whether a row renders a diff is decided at runtime by
 * the provider's heightMetrics hook (a tool_use OR tool_result can carry one), not
 * by its kind, so the diff view is always resolved and gated where the diff is.
 */
export interface ConsumedUiFlags {
  /** the row renders a collapsible RESULT body (see resultBodyCollapseKeyFor). */
  collapsed: boolean
  /** thinking / plan_execution / agent_prompt rows render an expandable bubble. */
  expanded: boolean
  /** a Bash tool_use renders its full multi-line command body when expanded (TOOL_USE_LAYOUT). */
  toolBodyExpanded: boolean
}

/** Kinds whose row renders an expandable thinking/plan/prompt bubble (the `expanded` flag). */
const EXPANDABLE_KINDS = new Set<string>(['assistant_thinking', 'plan_execution', 'agent_prompt'])

/**
 * The per-message UI key whose value collapses a row's RESULT body, or undefined for
 * a row with no collapsible result body. The SINGLE source of which key a body's
 * collapse reads, shared by the UI-state resolver (resolveRowUiState) and the height
 * estimator so an off-screen estimate can never assume a different key/default than
 * the row mounts with -- the same drift toolBodyExpandedKeyFor guards for the command
 * body.
 *
 * A plain tool_result, a Codex `commandExecution` (settled, renders ToolResultMessage),
 * and the ACP `execute`/`read`/`search`/`fetch` bodies all collapse on the shared
 * TOOL_RESULT_EXPANDED key (their bodies read getToolResultExpanded). A Codex
 * `collabAgentToolCall` prompt collapses on its own CODEX_COLLAB_AGENT_TOOL_CALL key.
 * A Codex MCP / webSearch row is `alwaysVisible` in its settled state (no collapse) --
 * its hook marks the estimate `uncollapsed` rather than reading a key here. Keyed on
 * the renderer-assigned toolName (distinct across providers: Codex `commandExecution`/
 * `collabAgentToolCall`, ACP lowercase kinds, Claude/Pi use capitalized / tool_result).
 */
export function resultBodyCollapseKeyFor(kind: string, toolName?: string): MessageUiKey | undefined {
  if (kind === 'tool_result')
    return MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
  if (toolName === 'commandExecution') // Codex, settled -> ToolResultMessage/CommandResultBody
    return MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
  if (toolName === 'collabAgentToolCall') // Codex collab prompt
    return MESSAGE_UI_KEY.CODEX_COLLAB_AGENT_TOOL_CALL
  if (toolName === 'execute' || toolName === 'read' || toolName === 'search' || toolName === 'fetch')
    return MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED // ACP tool_call_update bodies read getToolResultExpanded
  return undefined
}

/**
 * The per-message UI key whose value means "this tool row's full command/body is shown
 * expanded", or undefined for a row with no such toggle. Keyed on the tool name, which
 * is the renderer discriminator: Claude's `Bash` shows its multi-line command via
 * TOOL_USE_LAYOUT; an ACP `execute` tool_call_update shows its full command via
 * OPENCODE_TOOL_CALL_UPDATE (the key acpToolCallUpdateRenderer toggles). The SINGLE
 * source of which key a body-expand reads, shared by the UI-state resolver
 * (resolveRowUiState) and the height estimator, so an off-screen estimate can never
 * read a different key than the row mounts with -- the same drift expandedUiKeyFor
 * guards for the thinking/reasoning bubble.
 */
export function toolBodyExpandedKeyFor(toolName: string | undefined): MessageUiKey | undefined {
  if (toolName === 'Bash')
    return MESSAGE_UI_KEY.TOOL_USE_LAYOUT
  if (toolName === 'execute') // ACP_TOOL_KIND.EXECUTE -- distinct from Claude/Pi 'Bash' and Codex 'commandExecution'
    return MESSAGE_UI_KEY.OPENCODE_TOOL_CALL_UPDATE
  return undefined
}

/**
 * The UI flags a row of `kind` (+ `toolName` for tool_use rows) consumes. Accepts
 * both the bare classification kind and the estimator's `tool_use:<name>` prefixed
 * form -- the collapsed/expanded kinds are never prefixed, and the body-expand check
 * keys on `toolName`, so both callers pass what they have.
 */
export function uiFlagsConsumedBy(kind: string, toolName?: string): ConsumedUiFlags {
  return {
    collapsed: resultBodyCollapseKeyFor(kind, toolName) !== undefined,
    expanded: EXPANDABLE_KINDS.has(kind),
    toolBodyExpanded: toolBodyExpandedKeyFor(toolName) !== undefined,
  }
}

/**
 * The per-message UI key for a row's EXPAND toggle (the thinking/reasoning/plan/
 * agent-prompt bubble), resolved from the row's classification kind + provider.
 * The SINGLE source of this mapping: the height estimator (ChatView's
 * `expandedStateFor`, pre-mount) and the renderers (ThinkingBubble / AgentPromptView,
 * on-mount, via `RenderContext.expandUiKey`) both read it, so the off-screen
 * estimate can never assume a different key than the row mounts with -- the drift a
 * hand-synced copy on each side would invite. Codex reasoning renders under its own
 * CODEX_REASONING key (not the shared THINKING key Claude/Pi/ACP thinking uses);
 * plan_execution and agent_prompt have their own keys regardless of provider.
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
  if (provider === AgentProvider.CODEX)
    return MESSAGE_UI_KEY.CODEX_REASONING
  return MESSAGE_UI_KEY.THINKING
}

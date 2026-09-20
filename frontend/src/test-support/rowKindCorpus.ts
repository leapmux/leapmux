import type { MessageCategory } from '~/components/chat/messageClassifier'
import type { ChatRow } from '~/components/chat/model/row'
import type { ChatRowExtraction } from '~/components/chat/rowExtraction'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider as Provider } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotToolStart } from '~/test-support/copilotFixtures'

/**
 * One provider frame, with the row kind BOTH layers must answer for it.
 *
 * Layer 1 classifies a frame (`Provider.classify`) and layer 1 also extracts it
 * (`Provider.extractRow`). Two readers then take different answers from them: the
 * virtual list premeasures a row from the CATEGORY, and the transcript draws it from
 * the row model. A frame the two disagree about is measured as one kind of row and
 * drawn as another, which is a height the list reserved for the wrong thing.
 *
 * Each entry states the category explicitly rather than only comparing the two
 * answers. A frame whose shape is wrong classifies as `unknown` and extracts as
 * `unrecognized`, which AGREE -- so a test that compared the two alone would pass on
 * a corpus of nonsense.
 */
export interface RowKindCase {
  provider: AgentProvider
  /** A name for the case, used as the test title. */
  name: string
  payload: Record<string, unknown>
  /** What `classify` must answer. */
  category: MessageCategory['kind']
  /** The `span_type` column, which several providers read to identify a tool. */
  spanType?: string
  /**
   * The worker's own metadata column for this row.
   *
   * One category is decided from it alone: `classifyMessage` reads a saved control
   * answer out of `control_request_id` before it asks any plugin, because the row is
   * LeapMux's own record rather than a provider frame.
   */
  messageMetadata?: Record<string, unknown>
}

/**
 * The row kind that each classification must produce.
 *
 * The two vocabularies differ on purpose: a category says what KIND OF MESSAGE this
 * is, and a row kind says what the transcript DRAWS. Both halves of a tool span draw
 * one tool row, and both ways a reader's own text arrives draw one user row, so the
 * map is many-to-one in exactly two places.
 *
 * ONE category answers `hidden` for a reason of its own: a provider LeapMux does not
 * know has no plugin to ask, so nothing can read its frame. `MessageBubble` draws
 * that category itself -- the loud misconfiguration notice -- rather than the
 * unrecognized card the extraction answers for it. See {@link drawnRowKind}.
 *
 * `unknown` answers `unrecognized`, which no extractor RETURNS. An extractor that
 * cannot read a frame answers the `unsupported` OUTCOME, and `renderExtractedRow`
 * draws the shared unrecognized card for it.
 *
 * Every other category now draws from the row model. The turn end, the notification
 * thread and the control response each used to take a path around it -- a hook that
 * predated the model, or a branch in `MessageBubble` -- and each is extracted here now,
 * so one switch draws every row.
 */
export type DrawnRowKind = ChatRow['kind'] | 'unrecognized'

export const ROW_KIND_FOR_CATEGORY: Record<MessageCategory['kind'], DrawnRowKind> = {
  hidden: 'hidden',
  notification: 'notification',
  tool_use: 'tool',
  tool_result: 'tool',
  agent_prompt: 'agent-prompt',
  assistant_text: 'assistant-text',
  assistant_thinking: 'assistant-thinking',
  assistant_plan: 'assistant-plan',
  user_text: 'user',
  user_content: 'user',
  plan_execution: 'plan-execution',
  result_divider: 'divider',
  control_response: 'control-response',
  compact_summary: 'compact-summary',
  unknown: 'unrecognized',
  unsupported_provider: 'hidden',
}

/**
 * The one category `MessageBubble` draws ITSELF, outside the row model.
 *
 * An unsupported provider draws the loud misconfiguration notice, because only the
 * transcript knows to blame the tab's own metadata rather than the provider. The
 * extraction answers `unsupported` for it, truthfully -- nobody can read the frame --
 * and `MessageBubble` never calls the renderer for the category at all.
 *
 * A `hidden` category needs no entry: the extraction answers a real
 * `{ kind: 'hidden' }` ROW for it now, so every reader of layer 1 gets "this row
 * draws nothing" without knowing the category.
 */
const DRAWN_OUTSIDE_THE_CHAT_ROW = new Set<MessageCategory['kind']>(['unsupported_provider'])

/**
 * The row kind a reader SEES for one category and the outcome layer 1 answered.
 *
 * Two outcomes carry no row -- a frame nobody could read, and an extraction that
 * threw -- and `renderExtractedRow` substitutes `{ kind: 'unrecognized' }` for both,
 * so the reader gets the frame in a collapsed card. The one category `MessageBubble`
 * owns draws its own surface instead.
 *
 * A test that folded every rowless outcome into `hidden` would pass on a frame the
 * list measured as a tool call and the transcript drew as the fallback card -- which
 * is the exact disagreement this corpus exists to catch.
 */
export function drawnRowKind(category: MessageCategory['kind'], extraction: ChatRowExtraction): DrawnRowKind {
  if (extraction.kind === 'row')
    return extraction.row.kind
  return DRAWN_OUTSIDE_THE_CHAT_ROW.has(category) ? 'hidden' : 'unrecognized'
}

/**
 * The frames the invariant runs over.
 *
 * Every entry is a shape captured from a runtime or transcribed from the plugin's
 * own classification tests -- the Cursor and Goose entries come from the `.tmp/probe`
 * sessions this refactor ran against the installed binaries. A shape invented for
 * the test would prove only that two readers agree about a frame no provider sends.
 */
export const ROW_KIND_CASES: RowKindCase[] = [
  // --- Claude Code -------------------------------------------------------
  {
    provider: Provider.CLAUDE_CODE,
    name: 'assistant text',
    payload: { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'Done.' }] } },
    category: 'assistant_text',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'assistant thinking',
    payload: { type: 'assistant', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'Let me consider...', signature: 'sig' }] } },
    category: 'assistant_thinking',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'tool use',
    payload: { type: 'assistant', message: { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name: 'Read', input: { file_path: '/repo/a.ts' } }] } },
    category: 'tool_use',
    spanType: 'Read',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'tool result',
    payload: { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', content: 'file contents', tool_use_id: 'toolu_1' }] } },
    category: 'tool_result',
    spanType: 'Read',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'turn end',
    payload: { type: 'result', subtype: 'success', result: 'Done', duration_ms: 1234, num_turns: 1, stop_reason: 'end_turn' },
    category: 'result_divider',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'proposed plan',
    payload: {
      type: 'assistant',
      message: { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_2', name: 'ExitPlanMode', input: { plan: '1. Read the parser.\n2. Correct the token check.' } }] },
    },
    category: 'assistant_plan',
    spanType: 'ExitPlanMode',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'prompt to a subagent',
    payload: {
      type: 'user',
      parent_tool_use_id: 'toolu_3',
      message: { role: 'user', content: [{ type: 'text', text: 'Read the parser and report what it rejects.' }] },
    },
    category: 'agent_prompt',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'local command output',
    payload: { type: 'user', message: { role: 'user', content: 'Context: 42% used.' } },
    category: 'user_text',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'compact summary',
    payload: {
      type: 'user',
      isCompactSummary: true,
      message: { role: 'user', content: [{ type: 'text', text: 'The session corrected the token check.' }] },
    },
    category: 'compact_summary',
  },
  {
    // An assistant envelope whose content array holds no block the classifier knows.
    // The extractor finds no words in it either, so the reader gets the frame itself.
    provider: Provider.CLAUDE_CODE,
    name: 'unreadable assistant envelope',
    payload: { type: 'assistant', message: { role: 'assistant', content: [] } },
    category: 'unknown',
  },

  // --- Codex -------------------------------------------------------------
  {
    provider: Provider.CODEX,
    name: 'command item',
    payload: { item: { id: 'i1', type: 'commandExecution', command: 'ls -1', status: 'completed', aggregatedOutput: 'a.ts', exitCode: 0 } },
    category: 'tool_use',
    spanType: 'commandExecution',
  },
  {
    provider: Provider.CODEX,
    name: 'agent message item',
    payload: { item: { id: 'i2', type: 'agentMessage', text: 'All set.' } },
    category: 'assistant_text',
  },
  {
    provider: Provider.CODEX,
    name: 'turn completed',
    payload: { turn: { id: 't1', status: 'completed' } },
    category: 'result_divider',
  },

  // --- Cursor (captured from a live `cursor-agent acp` session) ----------
  {
    provider: Provider.CURSOR,
    name: 'tool call',
    payload: { sessionUpdate: 'tool_call', toolCallId: 'call-1', title: 'Update TODOs', kind: 'other', status: 'pending', rawInput: { _toolName: 'updateTodos' } },
    category: 'tool_use',
  },
  {
    provider: Provider.CURSOR,
    name: 'completed tool call',
    payload: { sessionUpdate: 'tool_call_update', toolCallId: 'call-1', status: 'completed' },
    category: 'tool_use',
  },

  // --- Goose (captured from a live `goose acp` session) ------------------
  {
    provider: Provider.GOOSE,
    name: 'read_image call',
    payload: {
      sessionUpdate: 'tool_call',
      toolCallId: 'call_948e',
      title: 'read image · /repo/dot.png',
      rawInput: { source: '/repo/dot.png' },
      _meta: { goose: { toolCall: { toolName: 'read_image', extensionName: 'developer' } } },
    },
    category: 'tool_use',
  },
  {
    provider: Provider.GOOSE,
    name: 'session info update',
    payload: { sessionUpdate: 'session_info_update', title: 'dot.png image size', updatedAt: '2026-09-16T07:06:19+00:00' },
    category: 'hidden',
  },

  // --- OpenCode and Kilo -------------------------------------------------
  {
    provider: Provider.OPENCODE,
    name: 'tool call',
    payload: { sessionUpdate: 'tool_call', toolCallId: 'oc-1', kind: 'read', title: 'read', status: 'pending', rawInput: { filePath: '/repo/a.ts' } },
    category: 'tool_use',
  },
  {
    provider: Provider.KILO,
    name: 'semantic search call',
    payload: { sessionUpdate: 'tool_call', toolCallId: 'kilo-1', kind: 'other', title: 'semantic_search', status: 'pending', rawInput: { query: 'the parser' } },
    category: 'tool_use',
  },

  // --- Pi ----------------------------------------------------------------
  {
    provider: Provider.PI,
    name: 'tool execution start',
    payload: { type: 'tool_execution_start', toolCallId: 'pi-1', toolName: 'read', args: { path: '/repo/a.ts' } },
    category: 'tool_use',
  },
  {
    provider: Provider.PI,
    name: 'tool execution end',
    payload: { type: 'tool_execution_end', toolCallId: 'pi-1', toolName: 'read', result: { output: 'file contents' } },
    category: 'tool_result',
    spanType: 'read',
  },
  {
    provider: Provider.PI,
    name: 'session entry',
    payload: { type: 'entry_appended', entry: { type: 'model_change', provider: 'anthropic', modelId: 'claude-opus-5' } },
    category: 'hidden',
  },
  {
    provider: Provider.PI,
    name: 'summarization retry',
    payload: { type: 'summarization_retry_scheduled', attempt: 1, maxAttempts: 3, delayMs: 500 },
    category: 'notification',
  },
  {
    provider: Provider.PI,
    name: 'turn end',
    payload: { type: 'agent_end', messages: [] },
    category: 'result_divider',
  },

  // --- Copilot -----------------------------------------------------------
  {
    provider: Provider.GITHUB_COPILOT,
    name: 'tool started',
    // Built by the same helper every Copilot test uses, so the corpus cannot drift
    // from the frame shape the rest of the suite exercises.
    payload: copilotToolStart('c1', 'view', { path: '/repo/a.ts' }),
    category: 'tool_use',
  },

  // --- ZCode -------------------------------------------------------------
  {
    provider: Provider.ZCODE,
    name: 'scheduled tool',
    payload: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'z1', toolName: 'Read', input: { file_path: '/repo/a.ts' } } },
    category: 'tool_use',
  },

  // --- The rows LeapMux writes itself ------------------------------------
  //
  // Each carries LeapMux's own envelope rather than a provider frame, so every
  // plugin reaches the same answer for it. One provider stands for all of them: a
  // case for each would repeat the shared branch and state nothing more.
  {
    provider: Provider.CLAUDE_CODE,
    name: 'user send',
    payload: { content: 'Correct the token check.', attachments: [{ filename: 'parser.ts', mime_type: 'text/plain' }] },
    category: 'user_content',
  },
  {
    provider: Provider.CLAUDE_CODE,
    name: 'plan sent into execution',
    payload: { content: 'Executing the approved plan.', planExecution: true },
    category: 'plan_execution',
  },
  {
    // The worker writes the answer as its own row and marks it with the id of the
    // request it answers. `classifyMessage` reads that column before it asks any
    // plugin, so the frame beside it can be anything the agent sent.
    provider: Provider.CLAUDE_CODE,
    name: 'saved control answer',
    payload: { response: { response: { behavior: 'allow' } } },
    messageMetadata: {
      [MESSAGE_METADATA_FIELD.ControlRequestID]: 'req-1',
      [MESSAGE_METADATA_FIELD.ControlRequestClaimToken]: 'claim-1',
    },
    category: 'control_response',
  },

  // --- A provider with no plugin -----------------------------------------
  {
    // UNSPECIFIED reaches the reader while a tab's worker metadata still loads, and
    // an unregistered provider would reach it the same way. Neither has a plugin to
    // read the frame, so the row model draws nothing and `MessageBubble` states the
    // misconfiguration itself.
    provider: Provider.UNSPECIFIED,
    name: 'frame of an unknown provider',
    payload: { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'Done.' }] } },
    category: 'unsupported_provider',
  },
]

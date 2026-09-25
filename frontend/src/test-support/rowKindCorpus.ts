import type { MessageCategory } from '~/components/chat/messageClassifier'
import type { ChatRow } from '~/components/chat/model/row'
import type { ChatRowExtraction } from '~/components/chat/rowExtraction'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { MIMO_STATUS_TYPE, MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider as Provider } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotToolStart } from '~/test-support/copilotFixtures'
import { compactionFrame, errorFrame, openingFrame, statusFrame, toolFrame } from '~/test-support/mimoFixtures'

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
 * own classification tests or tool fixtures -- the Cursor and Goose entries come from
 * sessions against the installed binaries. A shape invented for the test would prove
 * only that two readers agree about a frame no provider sends.
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

  // --- Reasonix (transcribed from `reasonix/toolResults.fixtures.ts`) -----
  {
    provider: Provider.REASONIX,
    name: 'first tool call',
    payload: { sessionUpdate: 'tool_call', toolCallId: 'rx-1', status: 'pending', title: 'read_file', kind: 'read', rawInput: { path: '/p/a.ts' } },
    category: 'tool_use',
  },
  {
    provider: Provider.REASONIX,
    name: 'completed tool call',
    payload: { sessionUpdate: 'tool_call_update', toolCallId: 'rx-1', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'one\ntwo\n' } }] },
    category: 'tool_use',
  },

  // --- Grok Build (captured from a live `grok agent stdio` session) -----
  {
    provider: Provider.GROK_BUILD,
    name: 'first tool call, named in _meta',
    payload: {
      sessionUpdate: 'tool_call',
      toolCallId: 'call_5_0',
      title: 'read_file',
      rawInput: { target_file: '/repo/probe.txt' },
      _meta: { 'x.ai/tool': { version: 1, name: 'read_file', kind: 'read', namespace: 'grok_build', label: 'Read', read_only: true } },
    },
    category: 'tool_use',
  },
  {
    provider: Provider.GROK_BUILD,
    name: 'end of a turn Grok started',
    payload: {
      jsonrpc: '2.0',
      method: '_x.ai/session_notification',
      params: { sessionId: 's', update: { sessionUpdate: 'turn_completed', prompt_id: 'p', stop_reason: 'end_turn', elapsed_ms: 264 } },
    },
    category: 'result_divider',
  },

  // --- Kiro (captured from a v3 `kiro-cli-chat acp` session) --------------
  {
    provider: Provider.KIRO,
    name: 'first tool call, identified by its title',
    payload: {
      sessionUpdate: 'tool_call',
      toolCallId: 't_read',
      title: 'Read File',
      kind: 'read',
      status: 'pending',
      rawInput: { path: '/w/hello.txt', offset: null, limit: null },
      locations: [{ path: '/w/hello.txt' }],
      _meta: { kiro: { toolOrigin: 'default' } },
    },
    category: 'tool_use',
  },
  {
    provider: Provider.KIRO,
    name: 'end of a turn Kiro started',
    payload: {
      sessionUpdate: 'session_info_update',
      _meta: { kiro: { turnEnd: { stopReason: 'end_turn' }, kind: 'turn_end', stopReason: 'end_turn', messageId: 'm-turn-end' } },
    },
    category: 'result_divider',
  },
  {
    provider: Provider.KIRO,
    name: 'context usage update',
    payload: {
      sessionUpdate: 'session_info_update',
      _meta: { kiro: { contextUsage: { usagePercentage: 0.8 }, kind: 'context_usage', usagePercentage: 0.8 } },
    },
    category: 'hidden',
  },

  // --- Qwen Code (captured from a live `qwen --acp` session) -------------
  {
    provider: Provider.QWEN_CODE,
    name: 'tool call, named in _meta',
    payload: {
      sessionUpdate: 'tool_call',
      toolCallId: 'call_e4a50b50ee',
      status: 'pending',
      title: 'Shell',
      content: [],
      locations: [],
      kind: 'execute',
      rawInput: {},
      _meta: { toolName: 'run_shell_command', provenance: 'builtin', phase: 'preparing' },
    },
    category: 'tool_use',
  },
  {
    provider: Provider.QWEN_CODE,
    name: 'end of a turn Qwen started',
    payload: { jsonrpc: '2.0', method: '_qwencode/end_turn', params: { sessionId: 's', reason: 'end_turn', source: 'goal' } },
    category: 'result_divider',
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

  // --- Oh My Pi -----------------------------------------------------------
  //
  // omp 18.2.11's own frames, from probes of the installed binary.
  {
    provider: Provider.OH_MY_PI,
    name: 'assistant text',
    payload: { type: 'message_end', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'The user wants a greeting.' }, { type: 'text', text: 'Hello from the mock model.' }], stopReason: 'stop' } },
    category: 'assistant_text',
  },
  {
    provider: Provider.OH_MY_PI,
    name: 'tool execution start',
    payload: { type: 'tool_execution_start', toolCallId: 'call_1', toolName: 'bash', args: { command: 'echo probe-output' } },
    category: 'tool_use',
  },
  {
    provider: Provider.OH_MY_PI,
    name: 'tool execution end',
    payload: { type: 'tool_execution_end', toolCallId: 'call_1', toolName: 'bash', result: { content: [{ type: 'text', text: 'probe-output\n\n\nWall time: 0.05 seconds' }], details: { timeoutSeconds: 300, wallTimeMs: 49.99 } }, isError: false },
    category: 'tool_result',
    spanType: 'bash',
  },
  {
    provider: Provider.OH_MY_PI,
    name: 'automatic retry',
    payload: { type: 'auto_retry_start', attempt: 1, maxAttempts: 10, delayMs: 92.36, errorMessage: '400 bad request' },
    category: 'notification',
  },
  {
    provider: Provider.OH_MY_PI,
    name: 'background job delivered',
    payload: { type: 'message_end', message: { role: 'custom', customType: 'async-result', content: '<system-notice>Background job ScoutOne has completed.</system-notice>', display: true, details: { jobs: [{ jobId: 'ScoutOne', type: 'task', label: 'ScoutOne' }] } } },
    category: 'notification',
  },
  {
    provider: Provider.OH_MY_PI,
    name: 'turn end',
    payload: { type: 'agent_end', isTerminal: true, messages: [] },
    category: 'result_divider',
  },

  // --- Amp ---------------------------------------------------------------
  //
  // Amp's own lines, from probes of the real CLI, as the worker cuts one assistant
  // message into one row for each block.
  {
    provider: Provider.AMP,
    name: 'assistant text',
    payload: { type: 'assistant', message: { type: 'message', role: 'assistant', content: [{ type: 'text', text: 'done' }], stop_reason: 'end_turn', usage: { input_tokens: 0, output_tokens: 7 } }, parent_tool_use_id: null, session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35' },
    category: 'assistant_text',
  },
  {
    provider: Provider.AMP,
    name: 'thinking',
    payload: { type: 'assistant', message: { type: 'message', role: 'assistant', content: [{ type: 'thinking', thinking: '**Planning shell ls execution**' }], stop_reason: null }, parent_tool_use_id: null, session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35' },
    category: 'assistant_thinking',
  },
  {
    provider: Provider.AMP,
    name: 'tool call',
    payload: { type: 'assistant', message: { type: 'message', role: 'assistant', content: [{ type: 'tool_use', id: 'TU-034UC14fL0WVIuQhmDl0qN', name: 'shell_command', input: { command: 'ls', workdir: '/work' } }], stop_reason: 'tool_use' }, parent_tool_use_id: null, session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35' },
    category: 'tool_use',
  },
  {
    provider: Provider.AMP,
    name: 'tool result',
    payload: { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'TU-034UC14fL0WVIuQhmDl0qN', content: '{"output":"README.md\\n","exitCode":0}', is_error: false }] }, parent_tool_use_id: null, session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35' },
    category: 'tool_result',
    spanType: 'shell_command',
  },
  {
    provider: Provider.AMP,
    name: 'turn end',
    payload: { type: 'result', subtype: 'success', is_error: false, num_turns: 4, result: 'pong', session_id: 'T-01a0d1c3-e51a-756b-9279-34c1cd441c35' },
    category: 'result_divider',
  },

  // --- Cline -------------------------------------------------------------
  //
  // Cline's own hub event envelopes, from probes of the real daemon, as the worker
  // persists them.
  {
    provider: Provider.CLINE,
    name: 'assistant text',
    payload: { version: 'v1', event: 'assistant.finished', sessionId: '1790258346189_zqp76', payload: { text: 'Hello from the mock model.' } },
    category: 'assistant_text',
  },
  {
    provider: Provider.CLINE,
    name: 'reasoning',
    payload: { version: 'v1', event: 'reasoning.finished', sessionId: '1790258346189_zqp76', payload: { reasoning: 'The user says hello. I answer briefly.' } },
    category: 'assistant_thinking',
  },
  {
    provider: Provider.CLINE,
    name: 'tool call',
    payload: { version: 'v1', event: 'tool.started', sessionId: '1790258346189_zqp76', payload: { toolCallId: 'call_bash_1', toolName: 'run_commands', input: { commands: ['echo probe-bash'] } } },
    category: 'tool_use',
  },
  {
    provider: Provider.CLINE,
    name: 'tool result',
    payload: { version: 'v1', event: 'tool.finished', sessionId: '1790258346189_zqp76', payload: { toolCallId: 'call_bash_1', toolName: 'run_commands', output: [{ query: 'echo probe-bash', result: 'probe-bash\n', success: true }] } },
    category: 'tool_result',
    spanType: 'run_commands',
  },
  {
    provider: Provider.CLINE,
    name: 'turn end',
    payload: { version: 'v1', event: 'run.completed', sessionId: '1790258346189_zqp76', payload: { reason: 'completed', result: { text: 'Hello from the mock model.', iterations: 1 } } },
    category: 'result_divider',
  },
  {
    provider: Provider.CLINE,
    name: 'compaction notice',
    payload: { version: 'v1', event: 'session.notice', sessionId: '1790258346189_zqp76', payload: { message: 'auto-compacted', noticeType: 'status', metadata: { kind: 'auto_compaction', phase: 'completed', tokensBefore: 90000, tokensAfter: 12000 } } },
    category: 'notification',
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

  // --- Codewhale ---------------------------------------------------------
  {
    provider: Provider.CODEWHALE,
    name: 'reply',
    payload: { event: 'item.completed', payload: { item: { kind: 'agent_message', status: 'completed', detail: 'Hello there.' } } },
    category: 'assistant_text',
  },
  {
    provider: Provider.CODEWHALE,
    name: 'reasoning',
    payload: { event: 'item.completed', payload: { item: { kind: 'agent_reasoning', status: 'completed', detail: 'Thinking.' } } },
    category: 'assistant_thinking',
  },
  {
    provider: Provider.CODEWHALE,
    name: 'tool started',
    payload: { event: 'item.started', payload: { item: { kind: 'tool_call', metadata: { tool_use_id: 'w1', tool_name: 'bash' } }, tool: { id: 'w1', name: 'bash', input: { command: 'ls' } } } },
    category: 'tool_use',
  },
  {
    provider: Provider.CODEWHALE,
    name: 'tool finished',
    payload: { event: 'item.completed', payload: { item: { kind: 'tool_call', detail: 'a.ts', metadata: { tool_use_id: 'w1', tool_name: 'bash', exit_code: 0 } } } },
    category: 'tool_result',
  },
  {
    provider: Provider.CODEWHALE,
    name: 'status',
    payload: { event: 'item.completed', payload: { item: { kind: 'status', status: 'completed', detail: 'Checkpoint saved' } } },
    category: 'notification',
  },
  {
    provider: Provider.CODEWHALE,
    name: 'turn end',
    payload: { event: 'turn.completed', payload: { turn: { status: 'completed' } } },
    category: 'result_divider',
  },

  // --- Kimi Code ---------------------------------------------------------
  //
  // Verbatim payloads of the 2.0.2 server, captured from a live session, which the
  // worker persists as they arrive.
  {
    provider: Provider.KIMI_CODE,
    name: 'tool call started',
    payload: {
      type: 'tool.call.started',
      time: 1790186809163,
      agentId: 'main',
      turnId: 0,
      toolCallId: 'call_bash_1',
      name: 'Bash',
      args: { command: 'echo hi-from-bash', description: 'Echo a greeting' },
      description: 'Running: echo hi-from-bash',
      display: { kind: 'command', command: 'echo hi-from-bash', cwd: '/work', description: 'Echo a greeting', language: 'bash' },
      sessionId: 'session_f7cf22a1-3d27-4d41-b8e2-978b1e5fa5a7',
    },
    category: 'tool_use',
  },
  {
    provider: Provider.KIMI_CODE,
    name: 'tool result',
    payload: { type: 'tool.result', time: 1790186809167, agentId: 'main', turnId: 0, toolCallId: 'call_bash_1', output: 'hi-from-bash\n', sessionId: 'session_f7cf22a1-3d27-4d41-b8e2-978b1e5fa5a7' },
    spanType: 'Bash',
    category: 'tool_result',
  },
  {
    provider: Provider.KIMI_CODE,
    name: 'turn end',
    payload: { type: 'turn.ended', time: 1790186809300, agentId: 'main', turnId: 0, reason: 'completed', durationMs: 32, sessionId: 'session_f7cf22a1-3d27-4d41-b8e2-978b1e5fa5a7' },
    category: 'result_divider',
  },

  // --- MiMo Code ---------------------------------------------------------
  //
  // Built by the same helpers every MiMo test uses. The shapes come from the probe of
  // the installed 0.1.14 server.
  {
    provider: Provider.MIMO_CODE,
    name: 'running tool',
    payload: openingFrame(MIMO_TOOL.Bash, { command: 'ls' }),
    category: 'tool_use',
    spanType: MIMO_TOOL.Bash,
  },
  {
    provider: Provider.MIMO_CODE,
    name: 'finished tool',
    payload: toolFrame(MIMO_TOOL.Bash, { input: { command: 'ls' }, output: 'a.ts\n', metadata: { output: 'a.ts\n', exit: 0 } }),
    category: 'tool_result',
    spanType: MIMO_TOOL.Bash,
  },
  {
    provider: Provider.MIMO_CODE,
    name: 'busy status',
    payload: statusFrame(MIMO_STATUS_TYPE.Busy),
    category: 'hidden',
  },
  {
    provider: Provider.MIMO_CODE,
    name: 'retry',
    payload: statusFrame(MIMO_STATUS_TYPE.Retry, { attempt: 1, message: 'Provider is overloaded', next: 0 }),
    category: 'notification',
  },
  {
    provider: Provider.MIMO_CODE,
    name: 'compaction',
    payload: compactionFrame(true),
    category: 'notification',
  },
  {
    provider: Provider.MIMO_CODE,
    name: 'turn end',
    payload: statusFrame(MIMO_STATUS_TYPE.Idle),
    category: 'result_divider',
  },
  {
    // The worker states the turn's tool count beside a turn end and nowhere else, so
    // the same event is a divider here and a notification in the next case.
    provider: Provider.MIMO_CODE,
    name: 'failed turn',
    payload: errorFrame('APIError', 'Provider returned 500'),
    messageMetadata: { [MESSAGE_METADATA_FIELD.ToolUses]: 0 },
    category: 'result_divider',
  },
  {
    provider: Provider.MIMO_CODE,
    name: 'error outside a turn',
    payload: errorFrame('APIError', 'Provider returned 500'),
    category: 'notification',
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

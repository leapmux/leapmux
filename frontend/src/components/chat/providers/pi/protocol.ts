/**
 * Pi wire-protocol vocabulary (frontend mirror).
 *
 * The Pi RPC stream is a JSONL feed where every envelope carries a
 * top-level `type`, and extension UI requests carry a `method`. The
 * frontend dispatches classification, rendering, and control responses
 * off these strings — centralizing them turns rename mistakes into
 * compile errors and gives the dispatch tables a single source of
 * truth. The literal values must match `backend/internal/worker/agent/
 * pi_protocol.go` byte-for-byte.
 */

export const PI_EVENT = {
  AgentStart: 'agent_start',
  AgentEnd: 'agent_end',
  TurnStart: 'turn_start',
  TurnEnd: 'turn_end',
  MessageStart: 'message_start',
  MessageUpdate: 'message_update',
  MessageEnd: 'message_end',
  ToolExecutionStart: 'tool_execution_start',
  ToolExecutionEnd: 'tool_execution_end',
  ToolExecutionUpdate: 'tool_execution_update',
  ExtensionUIRequest: 'extension_ui_request',
  ExtensionUIResponse: 'extension_ui_response',
  ExtensionError: 'extension_error',
  CompactionStart: 'compaction_start',
  CompactionEnd: 'compaction_end',
  AutoRetryStart: 'auto_retry_start',
  AutoRetryEnd: 'auto_retry_end',
  QueueUpdate: 'queue_update',
  Response: 'response',
} as const
export type PiEvent = typeof PI_EVENT[keyof typeof PI_EVENT]

/**
 * Pi assistant message-update sub-types. text_delta and thinking_delta
 * drive streaming UI; the start/stop/done/error variants bracket the
 * stream and are no-ops for rendering today.
 */
export const PI_ASSISTANT_EVENT = {
  TextDelta: 'text_delta',
  ThinkingDelta: 'thinking_delta',
} as const
export type PiAssistantEvent = typeof PI_ASSISTANT_EVENT[keyof typeof PI_ASSISTANT_EVENT]

/**
 * Pi extension_ui_request methods.
 *
 * Dialog methods (select / confirm / input / editor) block waiting for
 * an extension_ui_response; they surface as control requests in the
 * chat UI. Fire-and-forget methods drive session-info or notification
 * updates and never wait for a reply.
 */
export const PI_DIALOG_METHOD = {
  Select: 'select',
  Confirm: 'confirm',
  Input: 'input',
  Editor: 'editor',
} as const
export type PiDialogMethod = typeof PI_DIALOG_METHOD[keyof typeof PI_DIALOG_METHOD]

export const PI_EXTENSION_METHOD = {
  Notify: 'notify',
  SetStatus: 'setStatus',
  SetWidget: 'setWidget',
  SetTitle: 'setTitle',
  SetEditorText: 'set_editor_text',
} as const
export type PiExtensionMethod = typeof PI_EXTENSION_METHOD[keyof typeof PI_EXTENSION_METHOD]

/**
 * Pi tool names — the canonical lowercase identifiers Pi uses on
 * `tool_execution_start` / `tool_execution_end`. Renderer dispatch
 * keys off these.
 */
export const PI_TOOL = {
  Bash: 'bash',
  Read: 'read',
  Edit: 'edit',
  Write: 'write',
} as const
export type PiTool = typeof PI_TOOL[keyof typeof PI_TOOL]

/**
 * MiMo Code vocabulary that only the frontend consumes.
 *
 * The worker stores MiMo's own events verbatim, as `{type, properties}`: a tool
 * part's `message.part.updated`, the `session.status` and `session.error` that end a
 * turn, and the compaction part. Text and reasoning reach the transcript as the
 * worker's assembled messages instead, and the user's own messages as LeapMux's
 * `{content}` rows.
 *
 * Vocabulary that Go and TypeScript both consume belongs in
 * contracts/mimo-protocol.json. Import the generated values from
 * `~/generated/contracts/mimo-protocol`.
 */

/** The part fields the frontend reads beside the ones the contract states. */
export const MIMO_PART_FIELD = {
  /** The summary a compaction part carries once the compaction ends. */
  Projection: 'projection',
  /** True on a compaction that MiMo started by itself, when the context overflowed. */
  Auto: 'auto',
} as const

/**
 * The error names MiMo states in `session.error` that the divider words differently.
 *
 * An abort is the reader's own request, so the turn reads as interrupted rather than
 * failed.
 */
export const MIMO_ERROR_NAME = {
  Aborted: 'MessageAbortedError',
} as const

/**
 * The start of the sentence MiMo puts in a tool's `state.error` for a call that
 * never ran, because the reader refused it or dismissed it.
 *
 * MiMo states no refusal field, so the sentence is the only mark. A refused call is
 * DECLINED rather than failed: nothing ran, so no command produced an exit code.
 */
export const MIMO_DECLINED_ERRORS = [
  'The user rejected permission to use this specific tool call',
  'The user dismissed this question',
] as const

/** The sentence MiMo puts in `state.error` for a call that an abort cut short. */
export const MIMO_ABORTED_TOOL_ERROR = 'Tool execution aborted'

/**
 * The permissions that MiMo always asks for, whatever the rules say
 * (`FORCED_ASK` in MiMo's `permission/index.ts`).
 *
 * MiMo reads an `always` answer to one of them as `once`, so it saves nothing.
 */
export const MIMO_FORCED_ASK_PERMISSIONS: ReadonlySet<string> = new Set(['bash_delete'])

/**
 * The `key` of the one question that MiMo asks for an MCP server's confirmation
 * (MiMo HEAD, `mcp/elicitation.ts`; no release sends it yet).
 */
export const MIMO_QUESTION_KEY = {
  McpElicitation: 'mcp_elicitation',
} as const

/**
 * The title MiMo gives a `plan_exit` call that runs outside plan mode. MiMo asks
 * nobody then, and states `switched: false` as it does for a plan that the reader
 * sent back, so the title is the only word that separates the two.
 */
export const MIMO_PLAN_EXIT_OUTSIDE_PLAN_TITLE = 'Not in plan mode'

/**
 * The words the `exec` tool states in `metadata.status`. The call completes for
 * each of them, so this word is the only record of how the script ended.
 */
export const MIMO_EXEC_STATUS = {
  Completed: 'completed',
  Cancelled: 'cancelled',
  CodeError: 'code_error',
  Timeout: 'timeout',
  BudgetExceeded: 'budget_exceeded',
} as const

/** The notice MiMo's grep prints last when it could not read part of the tree. */
export const MIMO_GREP_SKIPPED_NOTICE = 'Some paths were inaccessible and skipped'

/** The operations of the `workflow` tool, which MiMo states as `operation`. */
export const MIMO_WORKFLOW_OPERATION = {
  Run: 'run',
  Status: 'status',
  Wait: 'wait',
  Cancel: 'cancel',
  Resume: 'resume',
} as const

/** The actions of the `cron` tool. */
export const MIMO_CRON_ACTION = {
  Schedule: 'schedule',
  Loop: 'loop',
  List: 'list',
  Get: 'get',
  Delete: 'delete',
  Rename: 'rename',
} as const

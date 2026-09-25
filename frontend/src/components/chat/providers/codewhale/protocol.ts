/**
 * Codewhale vocabulary that only the frontend consumes.
 *
 * The worker persists the runtime's OWN event envelopes byte for byte:
 * `{seq, event, thread_id, turn_id, item_id, payload}`. A turn item rides under
 * `payload.item`, and a tool call states its identity in the item's `metadata`.
 * A subagent's transcript arrives as one row for each content block, in the
 * record shape of Codewhale's own transcript file. User rows, control rows and
 * saved answers use LeapMux's own shapes.
 *
 * Vocabulary that Go and TypeScript both consume belongs in
 * contracts/codewhale-protocol.json: the envelope, item, transcript and block
 * fields, and the result fields that the worker acts on. Import the generated
 * values from `~/generated/contracts/codewhale-protocol`.
 */

/**
 * The fields of an item's `metadata` that only the browser reads.
 *
 * The contract holds the fields that identify a tool call and the result fields
 * that the worker reads too. These describe how a call ended, and nothing in the
 * worker reads them.
 */
export const CODEWHALE_RESULT_METADATA = {
  /** A file tool's change: `{diff, files:[{path, outcome}], renames:[{from, to}]}`. */
  Mutation: 'mutation',
  ExitCode: 'exit_code',
  DurationMs: 'duration_ms',
  IsError: 'is_error',
  /** Set on every result of `request_user_input`, whose answers the runtime removes. */
  ResponseRedacted: 'response_redacted',
} as const

/** The fields of a `mutation` record. */
export const CODEWHALE_MUTATION = {
  Diff: 'diff',
  Files: 'files',
  Renames: 'renames',
  Path: 'path',
  Outcome: 'outcome',
  From: 'from',
  To: 'to',
} as const

/** What one file of a `mutation` record underwent. */
export const CODEWHALE_FILE_OUTCOME = {
  Created: 'created',
  Updated: 'updated',
  Deleted: 'deleted',
} as const

/**
 * The opening sentence of the result that the runtime gives the FIRST call of a
 * deferred tool. That call loads the tool's schema and runs nothing.
 *
 * The main transcript marks such a call in the item's metadata. A subagent's
 * transcript is the model's own message history, whose result block states the
 * text alone, so this sentence is what marks the call there.
 */
export const CODEWHALE_DEFERRED_LOAD_SENTENCE = /^Tool `[^`\n]+` was deferred and has now been loaded\./

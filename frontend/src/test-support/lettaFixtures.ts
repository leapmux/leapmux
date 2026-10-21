/** Recorded Letta error and snapshot shapes for provider tests. */

/** The marker that the service error carries, as the E2E model script states it. */
export const LETTA_MODEL_ERROR_MARKER = 'NATIVEERRORMARKER'

const RUN_ID = 'local-run-1'

/** The service error that the local backend states for one failed request. */
function serviceErrorText(marker: string): string {
  return `400: ${JSON.stringify({ type: 'invalid_request_error', code: 'invalid_request_error', message: marker })}`
}

/** The `update_subagent_state` frame that opens a turn. It lists no subagent. */
export function lettaSubagentSnapshot(): Record<string, unknown> {
  return {
    type: 'update_subagent_state',
    subagents: [],
    runtime: { agent_id: 'agent-local-1', conversation_id: 'local-conv-1' },
    event_seq: 6,
    emitted_at: '2026-10-04T23:54:34.700Z',
    idempotency_key: 'update_subagent_state:6:dfc2de1b-5808-472d-a664-d3a5e2f4f67d',
  }
}

/**
 * The `delta` of a `loop_error` stream delta.
 *
 * `message` is the notice that Letta Code writes in its own transcript. For a local
 * backend error it is the indented JSON of the error. `api_error` repeats the
 * service error in a structured form.
 */
export function lettaLoopError(marker: string, isFinal: boolean): Record<string, unknown> {
  const text = serviceErrorText(marker)
  return {
    id: isFinal ? 'lifecycle-71f78419-3baa-451d-b74d-eada6d09b888' : 'lifecycle-db3808fb-7b10-4c86-bb1b-f6a5c4f9f980',
    date: '2026-10-04T23:54:35.364Z',
    message_type: 'loop_error',
    run_id: RUN_ID,
    message: JSON.stringify({ error: { error: { type: 'local_backend_error', message: text, detail: text }, run_id: RUN_ID } }, null, 2),
    stop_reason: 'error',
    is_terminal: isFinal,
    api_error: { message_type: 'error_message', message: text, error_type: 'local_backend_error', run_id: RUN_ID, detail: text },
  }
}

/** A mixed notification input with an unrecognized snapshot before a readable error. */
export function lettaMixedErrorThread(marker: string = LETTA_MODEL_ERROR_MARKER): Record<string, unknown>[] {
  return [lettaSubagentSnapshot(), lettaLoopError(marker, true)]
}

/** A failed turn carries the same native error body that its loop error stated. */
export function lettaFailedTurn(marker: string = LETTA_MODEL_ERROR_MARKER): Record<string, unknown> {
  return { type: 'turn_finished', turn_id: 'failed-turn', stop_reason: 'error', error: lettaLoopError(marker, true).message }
}

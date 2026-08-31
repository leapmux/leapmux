/**
 * ZCode wire-protocol vocabulary (frontend mirror).
 *
 * ZCode's app-server speaks line-delimited JSON with no `jsonrpc` field. Every
 * conversation row LeapMux persists is one SESSION EVENT ENVELOPE:
 *
 *   {eventId, sessionId, turnId, seq, timestamp, deliveryKind, type, payload}
 *
 * so the frontend dispatches on the envelope's `type` and reads the shapes out of
 * `payload`.
 *
 * The tables BOTH languages read live in contracts/zcode-protocol.json and reach this
 * plugin through `~/generated/contracts/zcode-protocol`, so they cannot drift from the
 * worker's copy. What stays here is the vocabulary the FRONTEND alone reads: no Go file
 * mentions `file_diff`, and of the three methods below only `session/stop` has a Go
 * twin. Adding a table here that the worker also reads puts it back in two places --
 * put it in the contract instead.
 */

/** The interaction methods the app-server asks LeapMux to answer. */
export const ZCODE_METHOD = {
  SessionStop: 'session/stop',
  RequestPermission: 'interaction/requestPermission',
  RequestUserInput: 'interaction/requestUserInput',
} as const

/**
 * `result.display.kind` — the app-server's own hint about how to draw a tool
 * result. `file_diff` is the only one it emits today, and it carries a
 * ready-to-render structured patch.
 */
export const ZCODE_DISPLAY = {
  FileDiff: 'file_diff',
} as const

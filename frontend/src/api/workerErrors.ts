import { Code, ConnectError } from '@connectrpc/connect'
import { ChannelError } from '~/lib/channelError'

/**
 * Header the Hub sets on a CodeUnavailable that is its own verdict about a
 * worker, rather than a transport fault that merely shares the code. Must match
 * `workerUnreachableMetaKey` in `backend/internal/hub/service/channel_service.go`.
 */
const WORKER_UNREACHABLE_META = 'leapmux-worker-unreachable'

/**
 * isWorkerUnreachable reports whether `err` describes a worker we
 * can't talk to — either the worker row is gone, the bearer has been
 * revoked from it, the hub-side handshake refused for an
 * existence/auth reason, or the E2EE channel that would carry the
 * call is not there. This is the predicate the tab-close fallback
 * uses to skip the worker-side prompt/RPC and still tombstone the
 * CRDT tab so the user isn't stuck with a stale row.
 *
 * Two error shapes reach it, because the call crosses two transports.
 *
 * `ConnectError` is the hub leg (`GetWorkerHandshakeParams` /
 * `OpenChannel`), and mirrors the CLI's `isWorkerUnreachable` in
 * `backend/internal/cli/remote/cmd/preflight.go`. Keep those two
 * predicates in sync: any CODE added here should be matched there.
 *
 * `ChannelError` with `source: 'transport'` is the E2EE leg, which
 * the CLI does not have — it is browser-only, so it has no CLI
 * counterpart to keep in sync. A worker that is registered but
 * offline drops its hub stream, the hub tears down every channel it
 * carried, and the in-flight or next call rejects with a
 * `ChannelError('transport', …)` ("channel closed by server",
 * "channel disconnected") rather than a connect code. Without this
 * arm the close was refused with "Failed to prepare tab close" and
 * the user had no way to retire the tab.
 *
 * The transport arm therefore needs `workerOnline === false` — a
 * POSITIVE offline reading from the worker list the hub pushed, not
 * merely a transport-shaped error. `'transport'` is far broader than
 * "this worker is offline": it also covers our own hub WebSocket
 * dropping, a WS-open timeout, an E2EE rekey timeout, the session
 * key passing its hard ceiling, a malformed hub reply, and any
 * non-ChannelError thrown on the send path (channelRpc coerces those
 * to `'transport'`). Several of those involve no network fault at
 * all, and the CRDT leg rides a different transport (Connect HTTP),
 * so it stays healthy and the tombstone commits — against a worker
 * that is up and answering. Since the caller uses this to SKIP the
 * uncommitted-work dialog and retire the tab, an unknown reading
 * must read as reachable: showing a failed probe the user can retry
 * beats silently retiring a tab whose worktree holds unsaved work.
 * The CLI's half is narrow in the same spirit — `preflight.go`
 * requires the `channel_open_failed` STAGE, not any connect-shaped
 * failure.
 *
 * Conservative on purpose, on BOTH legs. Transient connect failures
 * (timeouts, Internal, Unknown) do NOT match, and neither do the
 * non-transport ChannelError sources: `client` covers the per-RPC
 * timeout and an over-size payload, where the channel is healthy and
 * the agent is very likely alive; `rpc` / `stream` mean the worker
 * ANSWERED, which is the opposite of unreachable. Falling back on
 * those would tombstone a tab whose agent is running, which is far
 * worse than the user pressing retry.
 *
 * @param err the failure to classify.
 * @param workerOnline the hub's last-known liveness for the worker the
 * call targeted: `false` when the worker list positively reports it
 * offline, `true` when it reports it online, `undefined` when the list
 * has not mentioned it (still loading, or an unknown id).
 *
 * Required, not optional. It used to be optional, so a caller that forgot it got
 * `undefined`, the transport arm silently evaluated to false, and the
 * offline-close fallback stopped working with no type error anywhere.
 */
export function isWorkerUnreachable(err: unknown, workerOnline: boolean | undefined): boolean {
  // The fail-CLOSED reading, named once. Only a POSITIVE offline report unlocks a
  // path that retires a tab; `undefined` (list not arrived, unknown id) reads as
  // reachable, same as `true`.
  //
  // Deliberately a local on the resolved tri-state rather than a call into
  // workerLiveness: that module's policies take the worker LIST, and this function
  // is handed the already-resolved reading. Threading a pre-resolved boolean in
  // instead would collapse the tri-state at the boundary and leave two
  // same-shaped accessors with inverted meanings -- the hazard the accessor
  // renames exist to prevent.
  const knownOffline = workerOnline === false
  if (err instanceof ChannelError)
    return err.source === 'transport' && knownOffline
  if (!(err instanceof ConnectError))
    return false
  switch (err.code) {
    // NotFound is worker-specific by construction: the Hub is answering about
    // THIS worker id, and no transport fault produces it.
    case Code.NotFound:
      return true
    // Unavailable is trusted only when the Hub says it is a verdict about the
    // worker. The Hub tags its own "worker is offline" / "worker handshake
    // failed" answers (see workerUnreachableMetaKey in channel_service.go);
    // everything else wearing this code is transport-shaped -- an edge 503, a
    // proxy hiccup, the Hub restarting -- and says nothing about the worker, so
    // it needs the worker list to agree before we retire a tab on it.
    case Code.Unavailable:
      return err.metadata.get(WORKER_UNREACHABLE_META) === '1' || knownOffline
    // Neither of these is about the worker at all: the caller's own session or
    // bearer expired, or an ACL refused it. Requiring the positive offline
    // reading keeps a re-login prompt from committing a tombstone.
    case Code.Unauthenticated:
    case Code.PermissionDenied:
      return knownOffline
    default:
      return false
  }
}

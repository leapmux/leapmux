import { Code, ConnectError } from '@connectrpc/connect'

/**
 * isWorkerUnreachable reports whether `err` describes a worker we
 * can't talk to — either the worker row is gone, the bearer has been
 * revoked from it, or the hub-side handshake refused for an
 * existence/auth reason. This is the predicate the tab-close
 * fallback uses to skip the worker-side prompt/RPC and still
 * tombstone the CRDT tab so the user isn't stuck with a stale row.
 *
 * Mirrors the CLI's `isWorkerUnreachable` in
 * `backend/internal/cli/remote/cmd/preflight.go`. Keep the two
 * predicates in sync: any code added here should be matched there.
 *
 * Conservative on purpose. Transient transport failures (timeouts,
 * Internal, Unknown) do NOT match — falling back on those would
 * tombstone a tab whose agent is actually alive on the worker,
 * which is far worse than the user pressing retry. Match only the
 * codes that mean "this worker / channel really isn't available for
 * you to call": NotFound, PermissionDenied, Unauthenticated,
 * Unavailable.
 */
export function isWorkerUnreachable(err: unknown): boolean {
  if (!(err instanceof ConnectError))
    return false
  switch (err.code) {
    case Code.NotFound:
    case Code.PermissionDenied:
    case Code.Unauthenticated:
    case Code.Unavailable:
      return true
    default:
      return false
  }
}

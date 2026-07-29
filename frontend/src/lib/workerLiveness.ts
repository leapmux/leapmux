import type { Worker } from '~/generated/leapmux/v1/worker_pb'

/**
 * The hub's last-known liveness for one worker, as three states rather than
 * two: `true` online, `false` offline, `undefined` "the list has not
 * mentioned this id".
 *
 * Reads the list the Hub last pushed (`listWorkers`, refreshed on
 * WORKERS_CHANGED); it never opens a channel, because callers run on every
 * menu render and a probe there would surface as UI lag.
 *
 * The third state is the point of this function. An absent id means "the list
 * has not arrived yet" at least as often as it means "the machine is gone" --
 * on first paint the list is empty -- and the right answer for that differs by
 * caller, so this one refuses to guess. Each policy below picks its own
 * direction, and says why: fail-OPEN for gating affordances, fail-CLOSED for the
 * destructive paths. They live together so a new consumer picks a named policy
 * instead of re-deriving one and silently choosing the wrong default.
 */
export function workerOnlineState(workers: readonly Worker[], workerId: string): boolean | undefined {
  return workers.find(w => w.id === workerId)?.online
}

/**
 * Fail-OPEN policy, for gating UI affordances: unknown counts as online.
 *
 * Disabling a working action on a stale read is worse than letting the action
 * fail with an error the user can act on, so only a row whose worker is
 * present AND flagged offline is gated.
 */
export function isWorkerKnownOnline(workers: readonly Worker[], workerId: string): boolean {
  return workerOnlineState(workers, workerId) !== false
}

/**
 * Fail-CLOSED policy, for the DESTRUCTIVE paths: unknown counts as NOT offline.
 *
 * The mirror of isWorkerKnownOnline, and deliberately not its negation. A tab
 * close that judges a worker offline commits a CRDT tombstone without the
 * uncommitted-work dialog, and the worker's reconciler later reaps the worktree
 * -- so only a POSITIVE offline reading may unlock that path. An absent id (the
 * list has not arrived, or the worker is unknown) must read as reachable, because
 * showing a failed probe the user can retry beats silently retiring a tab whose
 * worktree holds work nobody saved.
 */
export function isWorkerKnownOffline(workers: readonly Worker[], workerId: string): boolean {
  return workerOnlineState(workers, workerId) === false
}

/**
 * The set of ids the list positively reports online, for callers that need to
 * test many ids at once rather than one per render.
 *
 * Same fail-open/fail-closed caveat as the two policies above: membership means
 * "known online", and absence conflates "known offline" with "not in the list".
 * A caller that must distinguish those has to use workerOnlineState instead.
 */
export function onlineWorkerIdSet(workers: readonly Worker[]): ReadonlySet<string> {
  return new Set(workers.filter(w => w.online).map(w => w.id))
}

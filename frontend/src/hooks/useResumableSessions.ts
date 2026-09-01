import type { Accessor } from 'solid-js'
import type { AgentProvider, AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal, on, untrack } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { createLogger } from '~/lib/logger'

const log = createLogger('useResumableSessions')

export interface UseResumableSessionsArgs {
  workerId: string
  workingDir: string
  agentProvider: AgentProvider
}

export interface UseResumableSessionsResult {
  sessions: Accessor<AgentSessionSummary[]>
  loading: Accessor<boolean>
  /**
   * Re-run the fetch for the current source.
   *
   * The source effect fires only when the worker, the directory or the
   * provider CHANGES, so a transient failure against the current three would
   * otherwise leave the field with no list and no way back until the user
   * changed one of them. No-op while the source is null.
   */
  refresh: () => Promise<void>
}

/**
 * The sessions the selected worker can offer to resume, for the selected
 * provider in the selected working directory.
 *
 * Plain signals plus an effect, NOT `createResource` — the router's Suspense
 * boundary unmounts the whole route while a resource loads, which flashes the
 * page blank under any dialog that reads it. `useAvailableShells` documents the
 * same constraint and takes the same shape.
 *
 * Three keys, not one. Shells depend on the worker alone; a session list
 * changes with the working directory the user picks in the tree AND with the
 * provider selector beside it, so all three are tracked and any change clears
 * the list before the next fetch lands. Showing the previous directory's
 * sessions while the new fetch is in flight would offer handles that belong to
 * somewhere else.
 *
 * A failed fetch leaves `sessions()` EMPTY rather than reporting an error. That
 * is what lets the field state one rule -- an empty list means offer the text
 * input -- instead of two, and it is honest: a worker that cannot answer and a
 * directory with no history both mean "nothing to pick here".
 */
export function useResumableSessions(
  source: Accessor<UseResumableSessionsArgs | null>,
): UseResumableSessionsResult {
  const [sessions, setSessions] = createSignal<AgentSessionSummary[]>([])

  const fetcher = createGuardedFetch<UseResumableSessionsArgs, Awaited<ReturnType<typeof workerRpc.listAgentSessions>>>({
    fetch: (args, signal) => workerRpc.listAgentSessions(args.workerId, {
      workerId: args.workerId,
      workingDir: args.workingDir,
      agentProvider: args.agentProvider,
    }, { signal }),
    applySuccess: resp => setSessions(resp.sessions),
    onError: (err) => {
      // Warn, not error: the worker answers this from another program's files,
      // so a failure here is a missing capability, not a broken dialog. The
      // field degrades to its text input and the user can still resume.
      log.warn('Failed to list resumable sessions', err)
      setSessions([])
    },
  })

  // The three keys are joined into one scalar so identity churn upstream -- a
  // caller that builds a fresh args object every tick -- does not re-fire the
  // effect when none of the three actually changed.
  //
  // A MEMO, not a plain accessor. `on(deps, fn)` re-runs `fn` whenever a signal
  // read inside `deps` updates; it does not compare what `deps` returned. A
  // dialog's inline closure re-reads other signals, so a bare accessor here
  // re-fired the effect -- and therefore the RPC -- on every unrelated tick.
  // A memo does compare, so the effect sees only a real change.
  //
  // JSON, not a delimiter: a working directory can contain any character a
  // separator might use, so `a` + `b c` and `a b` + `c` would produce one key
  // and the effect would miss a real change of directory.
  const sourceKey = createMemo((): string => {
    const args = source()
    if (args === null)
      return ''
    return JSON.stringify([args.workerId, args.workingDir, args.agentProvider])
  })

  createEffect(on(sourceKey, (key) => {
    // Clear FIRST, unconditionally: even when the next fetch never starts,
    // the list on screen must not keep describing the previous selection.
    setSessions([])
    if (key === '')
      return
    const args = untrack(source)
    if (args === null)
      return
    void fetcher.run(args)
  }))

  const refresh = async (): Promise<void> => {
    const args = untrack(source)
    if (args === null)
      return
    await fetcher.run(args)
  }

  return { sessions, loading: fetcher.loading, refresh }
}

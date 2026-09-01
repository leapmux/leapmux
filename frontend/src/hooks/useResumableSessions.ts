import type { Accessor } from 'solid-js'
import type { AgentProvider, AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { batch, createEffect, createMemo, createSignal, on, untrack } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { createLogger } from '~/lib/logger'
import { shallowEqual } from '~/lib/shallowEqual'

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
   * Whether a fetch for the CURRENT source has finished, whatever it returned.
   *
   * The field needs this to tell "the worker offered nothing" from "nobody has
   * asked yet". Those two states look identical through `sessions()` alone, and
   * a field that reads them as one swaps its control twice on every dialog
   * open: the text input paints first, because the effect has not run and
   * `loading()` is still false, and the menu replaces it a frame later.
   */
  settled: Accessor<boolean>
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
  const [settled, setSettled] = createSignal(false)

  const fetcher = createGuardedFetch<UseResumableSessionsArgs, Awaited<ReturnType<typeof workerRpc.listAgentSessions>>>({
    fetch: (args, signal) => workerRpc.listAgentSessions(args.workerId, {
      workerId: args.workerId,
      workingDir: args.workingDir,
      agentProvider: args.agentProvider,
    }, { signal }),
    applySuccess: (resp) => {
      batch(() => {
        setSessions(resp.sessions)
        setSettled(true)
      })
    },
    onError: (err) => {
      // Warn, not error: the worker answers this from another program's files,
      // so a failure here is a missing capability, not a broken dialog. The
      // field degrades to its text input and the user can still resume.
      log.warn('Failed to list resumable sessions', err)
      batch(() => {
        setSessions([])
        // A failure IS an answer for this source. Without it the field would
        // keep showing the menu for a fetch that already gave up, and the
        // refresh button -- the only way back -- sits beside a control the user
        // cannot type into.
        setSettled(true)
      })
    },
  })

  // A MEMO over the source itself, compared STRUCTURALLY. `on(deps, fn)`
  // re-runs `fn` whenever a signal read inside `deps` updates; it does not
  // compare what `deps` returned. A dialog's inline closure re-reads other
  // signals, so a bare accessor here re-fired the effect -- and therefore the
  // RPC -- on every unrelated tick. A memo with `equals` compares, so the
  // effect sees only a real change of worker, directory or provider.
  //
  // The memo yields the ARGS, not a string key. Encoding the three fields and
  // then reading `source` again through `untrack` to recover them made `''` a
  // second spelling of `null`, and that spelling is what let the effect return
  // early on the null path without telling the fetcher -- leaving an in-flight
  // request for the PREVIOUS directory alive to repopulate the list it had just
  // cleared. `shallowEqual` answers true for two nulls and false across a
  // null/object boundary, so the transition still fires exactly once.
  //
  // Seeded with `null`, not `undefined`: an `undefined` seed makes the first
  // comparison against a null source answer true, and the effect would never
  // run for a dialog that opens with nothing selected.
  const args = createMemo<UseResumableSessionsArgs | null>(source, null, { equals: shallowEqual })

  createEffect(on(args, (current) => {
    // Clear FIRST, unconditionally: even when the next fetch never starts,
    // the list on screen must not keep describing the previous selection.
    batch(() => {
      setSessions([])
      setSettled(false)
    })
    // `null` reaches the fetcher too. It aborts whatever is in flight, bumps
    // the generation so a late reply is discarded, and clears the loading flag
    // -- none of which happens if the effect just returns here.
    void fetcher.run(current)
  }))

  const refresh = async (): Promise<void> => {
    const current = untrack(args)
    if (current === null)
      return
    await fetcher.run(current)
  }

  return { sessions, loading: fetcher.loading, settled, refresh }
}

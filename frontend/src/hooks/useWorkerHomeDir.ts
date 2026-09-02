import type { Accessor } from 'solid-js'
import { createEffect, getOwner, on, onCleanup } from 'solid-js'
import { workerInfoStore } from '~/stores/workerInfo.store'

/**
 * One worker's home directory, warmed on demand.
 *
 * Every surface that shows a worker-side path tilde-compresses it against this
 * value, and `tildify` leaves the path absolute when it is empty — correct, but
 * long, and different from what the sidebar shows for the same directory. So a
 * dialog cannot simply READ the store: it has to make sure something filled it.
 *
 * Keyed on the ID, not on the mount. `LastTabCloseDialog` renders under a
 * deliberately non-keyed `<Show>`, so a second `open()` for a different worker
 * re-points the same component instance without remounting it — an `onMount`
 * fetch there would warm the first worker and never the second. `createEffect`
 * re-runs on the id and covers both.
 *
 * The store enforces its own freshness TTL and deduplicates an in-flight
 * request, so the usual warm entry costs no round trip.
 *
 * `disposed` gates the fetch for the same reason `createWorkerDialogContext`
 * does: a dialog closed during the E2EE handshake would otherwise keep the
 * round trip alive past dispose, and its late store write could roll back a
 * fresher value that a parallel dialog cached.
 */
export function useWorkerHomeDir(workerId: Accessor<string>): Accessor<string> {
  let disposed = false
  if (getOwner())
    onCleanup(() => { disposed = true })

  createEffect(on(workerId, (id) => {
    if (id && !disposed)
      void workerInfoStore.fetchWorkerInfo(id)
  }))

  return () => workerInfoStore.getHomeDir(workerId())
}

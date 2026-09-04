import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'

/**
 * Whether a worker runs on THIS machine, so a path it reports names a
 * directory the local Finder and the local editor can open.
 *
 * The one place that answers it, because the answer controls two bridge calls
 * that are silently wrong otherwise: `revealInFileManager` and `openInEditor`
 * always act locally, and a workspace's repository commonly lives on a remote
 * worker where the same absolute path either does not exist or -- worse --
 * exists and is a different directory.
 *
 * Two conditions, both required:
 *
 *  - `localSolo`: the desktop shell runs its own bundled sidecar, so "this
 *    machine" is a thing at all. A browser tab pointed at a remote hub has no
 *    local anything.
 *  - `autoRegistered`: the worker IS that bundled sidecar. A solo desktop can
 *    also hold registrations for remote machines, and those are not local.
 *
 * It lives here rather than in `~/lib/workerLiveness`, whose doc promises it
 * never probes anything beyond the `Worker` proto -- this asks the desktop
 * shell as well, through the caller.
 *
 * `localSolo` is a PARAMETER rather than a read, because `getRuntimeState()` is
 * async and reaches components through a `createResource`. A synchronous
 * accessor cannot read it.
 *
 * Do NOT add an `isTauriApp()` term. Its absence at `OpenInEditorButton` is
 * deliberate and documented: under `task dev-desktop` the webview points at
 * http://localhost:4328, so a URL check misclassifies a solo run as non-solo.
 * `localSolo` comes from the Rust shell's own capability flag and is the whole
 * gate.
 */
export function isLocalWorker(
  workers: readonly Worker[],
  workerId: string,
  localSolo: boolean,
): boolean {
  if (!localSolo || !workerId)
    return false
  return workers.find(w => w.id === workerId)?.autoRegistered === true
}

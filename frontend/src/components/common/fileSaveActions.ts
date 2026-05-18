import type { DownloadProgress } from '~/lib/fileDownload'
import type { PathFlavor } from '~/lib/paths'
import { createSignal } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { showWarnToast } from '~/components/common/Toast'
import { usePreferences } from '~/context/PreferencesContext'
import { downloadFileFromWorker, saveFileAs, saveFileToDownloads } from '~/lib/fileDownload'
import { basename } from '~/lib/paths'

/**
 * Which save/download operation is in flight.
 *
 *   - `'download'`        — web-mode anchor-click download
 *   - `'save-as'`         — desktop save-as dialog
 *   - `'save-to-downloads'` — desktop silent save to $DOWNLOAD
 */
export type FileSaveOp = 'download' | 'save-as' | 'save-to-downloads'

/**
 * In-flight save/download state. `kind` identifies which operation is
 * running; `progress` is a floored percent (0..100) once the worker
 * reports a total, or `null` before then (and during the save-as
 * dialog, where there's no transfer yet). The "idle" state lives
 * outside this type as a top-level `null` in `FileSaveActions.op`, so
 * `progress` is never spuriously meaningful while no save is running.
 */
export interface ActiveOp {
  kind: FileSaveOp
  progress: number | null
}

export interface FileSaveActions {
  /**
   * The in-flight op (kind + progress), or `null` when idle. Callers
   * drive per-button spinners off `op()?.kind === 'download'` etc. and
   * read `op()?.progress` for the percent.
   */
  op: () => ActiveOp | null
  /** Trigger a web-mode anchor-click download. No-op while busy. */
  handleDownload: () => void
  /** Trigger a desktop save-as dialog. No-op while busy. */
  handleSaveAs: () => void
  /** Trigger a desktop save-to-Downloads write. No-op while busy. */
  handleSaveToDownloads: () => void
}

export interface FileSaveActionsInput {
  workerId: () => string
  path: () => string
  flavor: () => PathFlavor
}

/**
 * Shared download/save-as/save-to-downloads workflow for a single file,
 * with re-entrancy guards and toast-on-error behavior.
 *
 * Used by `FileActionsMenu` (menu items) and `FileViewer` (the
 * `UnsupportedFileView` buttons). A single instance shared between both
 * surfaces keeps their busy state aligned — clicking "Save as" in the
 * menu disables the same action in the unsupported card and vice versa.
 *
 * Must be called inside a SolidJS reactive root (component body or
 * `createRoot`) so `usePreferences` and `createSignal` work.
 */
export function createFileSaveActions(input: FileSaveActionsInput): FileSaveActions {
  const prefs = usePreferences()
  const [op, setOp] = createSignal<ActiveOp | null>(null)

  const errLabel = () => basename(input.path(), input.flavor())

  interface OpSpec {
    fn: (
      workerId: string,
      path: string,
      flavor: PathFlavor,
      onProgress: DownloadProgress,
    ) => Promise<string | null | void>
    errPrefix: string
  }
  const OPS: Record<FileSaveOp, OpSpec> = {
    'download': { fn: downloadFileFromWorker, errPrefix: 'Failed to download' },
    'save-as': { fn: saveFileAs, errPrefix: 'Failed to save' },
    'save-to-downloads': { fn: saveFileToDownloads, errPrefix: 'Failed to save' },
  }

  const dispatch = (kind: FileSaveOp) => {
    if (op() !== null)
      return
    const { fn, errPrefix } = OPS[kind]
    setOp({ kind, progress: null })
    const label = errLabel()
    const onProgress: DownloadProgress = (received, total) => {
      // Skip until totalSize is known. The first worker response carries
      // it; before then there's nothing meaningful to show alongside the
      // spinner. `Math.min(100, …)` defends against a malformed worker
      // response that overshoots — pin the label at 100% rather than
      // render "101%".
      if (total <= 0)
        return
      const percent = Math.min(100, Math.floor((received / total) * 100))
      // Guard against a late emit after `.finally` cleared the op (e.g.
      // a worker response that races the rejection from a failed save).
      setOp(prev => prev === null ? null : { kind, progress: percent })
    }
    fn(input.workerId(), input.path(), input.flavor(), onProgress)
      .then((savedPath) => {
        // `saveFileAs` returns null when the user cancels the dialog;
        // the web download path returns void. Reveal only fires for
        // desktop saves that produced a path.
        if (savedPath && prefs.revealAfterDownload())
          void platformBridge.revealInFileManager(savedPath)
      })
      .catch((err) => {
        showWarnToast(`${errPrefix} ${label}`, err)
      })
      .finally(() => {
        setOp(null)
      })
  }

  return {
    op,
    handleDownload: () => dispatch('download'),
    handleSaveAs: () => dispatch('save-as'),
    handleSaveToDownloads: () => dispatch('save-to-downloads'),
  }
}

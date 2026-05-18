import type { PathFlavor } from '~/lib/paths'
import { createSignal } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { showWarnToast } from '~/components/common/Toast'
import { usePreferences } from '~/context/PreferencesContext'
import { downloadFileFromWorker, saveFileAs, saveFileToDownloads } from '~/lib/fileDownload'
import { basename } from '~/lib/paths'

/**
 * Which save/download operation is in flight (`null` when idle).
 *
 *   - `'download'`        — web-mode anchor-click download
 *   - `'save-as'`         — desktop save-as dialog
 *   - `'save-to-downloads'` — desktop silent save to $DOWNLOAD
 *
 * Callers use this to drive per-button spinners and to disable the
 * other action buttons while one is in flight.
 */
export type FileSaveOp = 'download' | 'save-as' | 'save-to-downloads' | null

export interface FileSaveActions {
  /** Non-null iff an op is in flight; identifies which one. */
  currentOp: () => FileSaveOp
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
  const [currentOp, setCurrentOp] = createSignal<FileSaveOp>(null)

  const errLabel = () => basename(input.path(), input.flavor())

  interface OpSpec {
    fn: (workerId: string, path: string, flavor: PathFlavor) => Promise<string | null | void>
    errPrefix: string
  }
  const OPS: Record<Exclude<FileSaveOp, null>, OpSpec> = {
    'download': { fn: downloadFileFromWorker, errPrefix: 'Failed to download' },
    'save-as': { fn: saveFileAs, errPrefix: 'Failed to save' },
    'save-to-downloads': { fn: saveFileToDownloads, errPrefix: 'Failed to save' },
  }

  const dispatch = (op: Exclude<FileSaveOp, null>) => {
    if (currentOp() !== null)
      return
    const { fn, errPrefix } = OPS[op]
    setCurrentOp(op)
    const label = errLabel()
    fn(input.workerId(), input.path(), input.flavor())
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
        setCurrentOp(null)
      })
  }

  return {
    currentOp,
    handleDownload: () => dispatch('download'),
    handleSaveAs: () => dispatch('save-as'),
    handleSaveToDownloads: () => dispatch('save-to-downloads'),
  }
}

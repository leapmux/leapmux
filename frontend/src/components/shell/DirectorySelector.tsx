import type { Accessor, Component } from 'solid-js'
import type { DirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import House from 'lucide-solid/icons/house'
import { createEffect, createMemo, createSignal, onCleanup, Show } from 'solid-js'
import { treeContainer, treeContainerFill } from '~/components/common/Dialog.css'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { usePreferences } from '~/context/PreferencesContext'
import { useFilesystemRoots } from '~/hooks/useFilesystemRoots'
import { formatErrorMessage } from '~/lib/errors'
import { registerDialogFileTreeOps } from '~/lib/fileTreeOps'
import { filesystemRoot, flavorFromOs } from '~/lib/paths'
import { shortcutHint } from '~/lib/shortcuts/display'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { emptyState } from '~/styles/shared.css'
import { DriveSelector } from './DriveSelector'
import { PathInput } from './PathInput'

/**
 * Narrow slice of `WorkerDialogContext` that `DirectorySelector` reads —
 * the worker id and the path signals. See `WorkerSelectorState` for the
 * rationale (component-owned interfaces keep the structural surface
 * stable across parent-state additions). Worker metadata (homeDir, OS)
 * is read from the module-scope {@link workerInfoStore} singleton.
 */
export interface DirectorySelectorState {
  workerId: Accessor<string>
  workingDir: Accessor<string>
  setWorkingDir: (path: string) => void
}

interface DirectorySelectorProps {
  state: DirectorySelectorState
  tree: DirectoryTreeState
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const DirectorySelector: Component<DirectorySelectorProps> = (props) => {
  // The hidden-files toggle is a reactive preference (the settings dialog's
  // Files & Applications group edits the same key), so it reads through the
  // preferences context instead of a local persisted signal.
  const prefs = usePreferences()
  const showHiddenFiles = prefs.directoryPickerShowHidden
  const setShowHiddenFiles = (next: (prev: boolean) => boolean) => prefs.setDirectoryPickerShowHidden(next(showHiddenFiles()))

  const workerId = () => props.state.workerId()
  const homeDir = () => workerInfoStore.getHomeDir(workerId())
  // Undefined until the worker reports, NOT `flavorFromOs(undefined)`, which
  // answers `'posix'`. That answer now picks the tree's ROOT, so a Windows
  // worker whose info has not arrived would mount a tree at `/`, issue a
  // ListDirectory the worker refuses, and paint an error over the whole pane
  // before it re-roots at `C:\`. Five other call sites already treat unknown
  // as its own state -- `workerPaths.ts` is the named helper.
  const flavor = createMemo(() => {
    const os = workerInfoStore.getOs(workerId())
    return os ? flavorFromOs(os) : undefined
  })

  // Windows only. `filesystemRoot` already knows a POSIX worker has exactly
  // one root, so the round trip would buy nothing there. Keying on the
  // REPORTED os is also what keeps WSL and Docker workers out: both report
  // `linux`, and both really do have a single `/`.
  const [drivesError, setDrivesError] = createSignal<string | null>(null)
  const drives = useFilesystemRoots(
    workerId,
    () => flavor() === 'win32',
    // Without this the picker shows "Loading drives…" for a fetch that already
    // failed, and only the Refresh button escapes -- with nothing on screen
    // saying to press it.
    (err: unknown) => setDrivesError(formatErrorMessage(err, 'Failed to list drives')),
  )

  /**
   * The tree's root: the filesystem root of whatever the picker points at.
   *
   * DERIVED, never stored. A signal holding "the current drive" would be a
   * second statement of the same fact, and the two would disagree the moment a
   * user typed another drive into the path box -- the box writes
   * `workingDir`, and nothing would have written that signal.
   *
   * The fallback chain is the selection, then the worker's home directory (a
   * "New workspace" started from a directory opens with NO selection), then
   * what the worker reported. Undefined until one of those answers.
   *
   * An unknown flavor does NOT stop the first two links: `filesystemRoot`
   * sniffs the path itself, so a prefilled `C:\proj` still roots at `C:\` and
   * `/repo/sub` at `/`. Only a worker with no selection AND no home directory
   * waits, and then the placeholder below is the honest answer.
   */
  const treeRoot = createMemo(() => {
    const f = flavor()
    return filesystemRoot(props.state.workingDir(), f)
      ?? filesystemRoot(homeDir(), f)
      ?? (f === 'posix' ? '/' : f === 'win32' ? drives.roots()[0] : undefined)
  })

  /**
   * The root the drive menu shows, or undefined when there is no menu.
   *
   * Only when there is a CHOICE to make. A POSIX worker reports exactly one
   * root, so this one rule hides the control on every platform with nothing
   * to offer, and the layout needs no OS test of its own.
   */
  const driveMenuRoot = createMemo(() => (drives.roots().length > 1 ? treeRoot() : undefined))

  // Both refresh entry points do the same thing, so a user never ends up with
  // a re-listed tree beside a stale drive menu. `refresh` is a no-op while the
  // source is null, so a POSIX worker's Refresh costs nothing extra.
  const refreshAll = () => {
    setDrivesError(null)
    props.tree.refreshTree()
    void drives.refresh()
  }

  /**
   * Select the home directory and open it.
   *
   * Two steps, in this order. The selection alone only reveals the directory,
   * because the tree opens the ancestors of its reveal target and stops there.
   * And the selection can re-root the tree -- a home directory on another
   * Windows drive does exactly that -- which replaces the expansion state, so
   * an expand written first would not survive.
   */
  const goHome = () => {
    const home = homeDir()
    if (!home)
      return
    props.state.setWorkingDir(home)
    props.tree.expandTreePath(home)
  }

  createEffect(() => {
    const unregister = registerDialogFileTreeOps({
      refresh: refreshAll,
      toggleHiddenFiles: () => setShowHiddenFiles(prev => !prev),
    })
    onCleanup(unregister)
  })

  return (
    <LabeledField
      class="vstack gap-1"
      label="Working Directory"
      actions={(
        <>
          <IconButton
            icon={showHiddenFiles() ? Eye : EyeOff}
            iconSize="sm"
            size="sm"
            title={shortcutHint(showHiddenFiles() ? 'Hide hidden files' : 'Show hidden files', 'app.toggleHiddenFiles')}
            state={showHiddenFiles() ? IconButtonState.Enabled : IconButtonState.Active}
            onClick={() => setShowHiddenFiles(prev => !prev)}
            data-testid="directory-selector-show-hidden-toggle"
          />
          <IconButton
            icon={House}
            iconSize="sm"
            size="sm"
            title="Go to home directory"
            state={homeDir() ? IconButtonState.Enabled : IconButtonState.Disabled}
            onClick={goHome}
            data-testid="directory-selector-home"
          />
          <RefreshButton
            onClick={refreshAll}
            title={shortcutHint('Refresh directory tree', 'app.refreshDirectoryTree')}
            data-testid="directory-selector-refresh"
          />
        </>
      )}
    >
      <Show
        when={props.state.workerId()}
        fallback={(
          <div class={treeContainer}>
            <div class={`${treeContainerFill} ${emptyState}`}>No workers online. Connect a worker to browse directories.</div>
          </div>
        )}
      >
        <div class={treeContainer}>
          <PathInput
            selectedPath={props.state.workingDir()}
            homeDir={homeDir()}
            flavor={flavor()}
            onSubmit={props.state.setWorkingDir}
            leading={(
              <Show when={driveMenuRoot()}>
                {root => (
                  <DriveSelector
                    value={root()}
                    roots={drives.roots()}
                    onSelect={props.state.setWorkingDir}
                  />
                )}
              </Show>
            )}
          />
          <Show
            when={treeRoot()}
            fallback={(
              <div class={`${treeContainerFill} ${emptyState}`} data-testid="directory-selector-no-root">
                {drivesError() ?? 'Loading drives…'}
              </div>
            )}
          >
            {root => (
              <DirectoryTree
                workerId={props.state.workerId()}
                selectedPath={props.state.workingDir()}
                onSelect={props.state.setWorkingDir}
                rootPath={root()}
                revealPath={homeDir()}
                homeDir={homeDir()}
                flavor={flavor()}
                showHiddenFiles={showHiddenFiles()}
                gitStatusStore={props.repoGitStore}
                showGitStatus={false}
                ref={props.tree.setTreeRef}
              />
            )}
          </Show>
        </div>
      </Show>
    </LabeledField>
  )
}

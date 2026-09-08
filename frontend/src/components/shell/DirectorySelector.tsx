import type { Accessor, Component } from 'solid-js'
import type { DirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import { createEffect, createMemo, onCleanup, Show } from 'solid-js'
import { treeContainer, treeContainerFill } from '~/components/common/Dialog.css'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { usePreferences } from '~/context/PreferencesContext'
import { useFilesystemRoots } from '~/hooks/useFilesystemRoots'
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
  const flavor = createMemo(() => flavorFromOs(workerInfoStore.getOs(workerId())))

  // Windows only. `filesystemRoot` already knows a POSIX worker has exactly
  // one root, so the round trip would buy nothing there. Keying on the
  // REPORTED os is also what keeps WSL and Docker workers out: both report
  // `linux`, and both really do have a single `/`.
  const drives = useFilesystemRoots(() => {
    const id = workerId()
    return id && flavor() === 'win32' ? { workerId: id } : null
  })

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
   * what the worker reported. Undefined on Windows alone, and only until one
   * of those answers.
   */
  const treeRoot = createMemo(() => {
    const f = flavor()
    return filesystemRoot(props.state.workingDir(), f)
      ?? filesystemRoot(homeDir(), f)
      ?? (f === 'posix' ? '/' : drives.roots()[0])
  })

  // Both refresh entry points do the same thing, so a user never ends up with
  // a re-listed tree beside a stale drive menu. `refresh` is a no-op while the
  // source is null, so a POSIX worker's Refresh costs nothing extra.
  const refreshAll = () => {
    props.tree.refreshTree()
    void drives.refresh()
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
              // Only when there is a choice to make. A POSIX worker reports
              // exactly one root, so this one rule hides the control on every
              // platform with nothing to offer, and the layout needs no OS test
              // of its own.
              <Show when={drives.roots().length > 1 && treeRoot()}>
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
            fallback={<div class={`${treeContainerFill} ${emptyState}`}>Loading drives…</div>}
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

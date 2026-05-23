import type { Accessor, Component } from 'solid-js'
import type { DirectoryTreeState } from '~/hooks/createDirectoryTreeState'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import { createEffect, createSignal, on, onCleanup, Show } from 'solid-js'
import { labelRow, treeContainer } from '~/components/common/Dialog.css'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import { registerDialogFileTreeOps } from '~/lib/fileTreeOps'
import { flavorFromOs } from '~/lib/paths'
import { shortcutHint } from '~/lib/shortcuts/display'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { emptyState } from '~/styles/shared.css'

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
}

export const DirectorySelector: Component<DirectorySelectorProps> = (props) => {
  const [showHiddenFiles, setShowHiddenFiles] = createSignal(localStorageGet<boolean>(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN) ?? true)

  createEffect(on(showHiddenFiles, (value) => {
    localStorageSet(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, value)
  }, { defer: true }))

  createEffect(() => {
    const unregister = registerDialogFileTreeOps({
      refresh: () => props.tree.refreshTree(),
      toggleHiddenFiles: () => setShowHiddenFiles(prev => !prev),
    })
    onCleanup(unregister)
  })

  return (
    <div class="vstack gap-1">
      <div class={labelRow}>
        Working Directory
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
          onClick={() => props.tree.refreshTree()}
          title={shortcutHint('Refresh directory tree', 'app.refreshDirectoryTree')}
          data-testid="directory-selector-refresh"
        />
      </div>
      <Show
        when={props.state.workerId()}
        fallback={(
          <div class={treeContainer}>
            <div class={emptyState}>No workers online. Connect a worker to browse directories.</div>
          </div>
        )}
      >
        <div class={treeContainer}>
          <DirectoryTree
            workerId={props.state.workerId()}
            selectedPath={props.state.workingDir()}
            onSelect={props.state.setWorkingDir}
            rootPath="~"
            homeDir={workerInfoStore.getHomeDir(props.state.workerId())}
            flavor={flavorFromOs(workerInfoStore.getOs(props.state.workerId()))}
            showHiddenFiles={showHiddenFiles()}
            ref={props.tree.setTreeRef}
          />
        </div>
      </Show>
    </div>
  )
}

import type { Component } from 'solid-js'
import type { WorkerDialogState } from '~/hooks/createWorkerDialogState'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import { createEffect, createSignal, on, onCleanup, Show } from 'solid-js'
import { labelRow, treeContainer } from '~/components/common/Dialog.css'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, safeGetJson, safeSetJson } from '~/lib/browserStorage'
import { flavorFromOs } from '~/lib/paths'
import { registerDialogFileTreeOps } from '~/lib/fileTreeOps'
import { shortcutHint } from '~/lib/shortcuts/display'
import { emptyState } from '~/styles/shared.css'

interface DirectorySelectorProps {
  state: WorkerDialogState
}

export const DirectorySelector: Component<DirectorySelectorProps> = (props) => {
  const [showHiddenFiles, setShowHiddenFiles] = createSignal(safeGetJson<boolean>(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN) ?? true)

  createEffect(on(showHiddenFiles, (value) => {
    safeSetJson(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, value)
  }, { defer: true }))

  createEffect(() => {
    const unregister = registerDialogFileTreeOps({
      refresh: () => props.state.refreshTree(),
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
          iconSize="xs"
          size="sm"
          title={shortcutHint(showHiddenFiles() ? 'Hide hidden files' : 'Show hidden files', 'app.toggleHiddenFiles')}
          state={showHiddenFiles() ? IconButtonState.Enabled : IconButtonState.Active}
          onClick={() => setShowHiddenFiles(prev => !prev)}
          data-testid="directory-selector-show-hidden-toggle"
        />
        <RefreshButton
          onClick={() => props.state.refreshTree()}
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
            homeDir={props.state.workerInfoStore.getHomeDir(props.state.workerId())}
            flavor={flavorFromOs(props.state.workerInfoStore.getOs(props.state.workerId()))}
            showHiddenFiles={showHiddenFiles()}
            ref={props.state.treeRef}
          />
        </div>
      </Show>
    </div>
  )
}

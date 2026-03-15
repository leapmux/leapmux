import type { Component } from 'solid-js'
import type { WorkerDialogState } from '~/hooks/createWorkerDialogState'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import { createEffect, createSignal, Show } from 'solid-js'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { safeGetJson, safeSetJson } from '~/lib/safeStorage'
import { labelRow, treeContainer } from '~/styles/shared.css'

interface DirectorySelectorProps {
  state: WorkerDialogState
}

const SHOW_HIDDEN_KEY = 'directorySelector:showHidden'

export const DirectorySelector: Component<DirectorySelectorProps> = (props) => {
  const [showHiddenFiles, setShowHiddenFiles] = createSignal(safeGetJson<boolean>(SHOW_HIDDEN_KEY) ?? true)

  createEffect(() => {
    safeSetJson(SHOW_HIDDEN_KEY, showHiddenFiles())
  })

  return (
    <div>
      <div class={labelRow}>
        Working Directory
        <IconButton
          icon={showHiddenFiles() ? Eye : EyeOff}
          iconSize="xs"
          size="sm"
          title={showHiddenFiles() ? 'Hide hidden files' : 'Show hidden files'}
          state={showHiddenFiles() ? IconButtonState.Enabled : IconButtonState.Active}
          onClick={() => setShowHiddenFiles(prev => !prev)}
          data-testid="directory-selector-show-hidden-toggle"
        />
        <RefreshButton onClick={() => props.state.refreshTree()} title="Refresh directory tree" />
      </div>
      <Show when={props.state.workerId()}>
        <div class={treeContainer}>
          <DirectoryTree
            workerId={props.state.workerId()}
            selectedPath={props.state.workingDir()}
            onSelect={props.state.setWorkingDir}
            rootPath="~"
            homeDir={props.state.workerInfoStore.getHomeDir(props.state.workerId())}
            showHiddenFiles={showHiddenFiles()}
            ref={props.state.treeRef}
          />
        </div>
      </Show>
    </div>
  )
}

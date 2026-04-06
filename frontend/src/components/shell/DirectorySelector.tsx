import type { Component } from 'solid-js'
import type { WorkerDialogState } from '~/hooks/createWorkerDialogState'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import { createEffect, createSignal, on, Show } from 'solid-js'
import { labelRow, treeContainer } from '~/components/common/Dialog.css'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { safeGetJson, safeSetJson } from '~/lib/browserStorage'
import { emptyState } from '~/styles/shared.css'

interface DirectorySelectorProps {
  state: WorkerDialogState
}

const SHOW_HIDDEN_KEY = 'directorySelector:showHidden'

export const DirectorySelector: Component<DirectorySelectorProps> = (props) => {
  const [showHiddenFiles, setShowHiddenFiles] = createSignal(safeGetJson<boolean>(SHOW_HIDDEN_KEY) ?? true)

  createEffect(on(showHiddenFiles, (value) => {
    safeSetJson(SHOW_HIDDEN_KEY, value)
  }, { defer: true }))

  return (
    <div class="vstack gap-1">
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
            showHiddenFiles={showHiddenFiles()}
            ref={props.state.treeRef}
          />
        </div>
      </Show>
    </div>
  )
}

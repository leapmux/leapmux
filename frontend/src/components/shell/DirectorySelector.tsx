import type { Component } from 'solid-js'
import type { WorkerDialogState } from '~/hooks/createWorkerDialogState'
import { Show } from 'solid-js'
import { RefreshButton } from '~/components/common/RefreshButton'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { labelRow, treeContainer } from '~/styles/shared.css'

interface DirectorySelectorProps {
  state: WorkerDialogState
}

export const DirectorySelector: Component<DirectorySelectorProps> = (props) => {
  return (
    <label>
      <div class={labelRow}>
        Working Directory
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
            ref={props.state.treeRef}
          />
        </div>
      </Show>
    </label>
  )
}

import type { Component } from 'solid-js'
import type { WorkerDialogState } from '~/hooks/createWorkerDialogState'
import { For, Show } from 'solid-js'
import { RefreshButton } from '~/components/common/RefreshButton'
import { labelRow } from '~/styles/shared.css'

interface WorkerSelectorProps {
  state: WorkerDialogState
}

export const WorkerSelector: Component<WorkerSelectorProps> = (props) => {
  return (
    <label>
      <div class={labelRow}>
        Worker
        <RefreshButton onClick={props.state.handleRefresh} disabled={props.state.refreshing()} title="Refresh workers" />
      </div>
      <select
        value={props.state.workerId()}
        onChange={e => props.state.setWorkerId(e.currentTarget.value)}
      >
        <Show when={props.state.workers().length === 0}>
          <option value="">No workers online</option>
        </Show>
        <For each={props.state.workers()}>
          {(b) => {
            const info = () => props.state.workerInfoStore.workerInfo(b.id)
            const label = () => {
              const i = info()
              if (!i)
                return b.id
              const details = [i.version, i.os, i.arch].filter(Boolean).join(', ')
              return details ? `${i.name} (${details})` : i.name
            }
            return <option value={b.id}>{label()}</option>
          }}
        </For>
      </select>
    </label>
  )
}

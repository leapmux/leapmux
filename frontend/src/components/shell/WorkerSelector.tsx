import type { Accessor, Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { For, Show } from 'solid-js'
import { labelRow } from '~/components/common/Dialog.css'
import { RefreshButton } from '~/components/common/RefreshButton'
import { workerInfoStore } from '~/stores/workerInfo.store'

/**
 * Narrow slice of `WorkerDialogContext` that `WorkerSelector` actually
 * reads. Defined here so adding a field to the parent state doesn't
 * silently reach into this component, and so unit tests can pass a
 * stub matching just this shape. Worker metadata is read from the
 * module-scope {@link workerInfoStore} singleton rather than threaded
 * via this slice — see workerInfo.store.ts for the rationale.
 */
export interface WorkerSelectorState {
  workerId: Accessor<string>
  setWorkerId: (id: string) => void
  workers: Accessor<Worker[]>
  workersRefreshing: Accessor<boolean>
  refreshWorkers: () => Promise<void> | void
  prefetchOnlineWorkerInfos: () => void
}

interface WorkerSelectorProps {
  state: WorkerSelectorState
}

export const WorkerSelector: Component<WorkerSelectorProps> = (props) => {
  return (
    <div>
      <div class={labelRow}>
        Worker
        <RefreshButton onClick={props.state.refreshWorkers} disabled={props.state.workersRefreshing()} title="Refresh workers" />
      </div>
      <select
        value={props.state.workerId()}
        onChange={e => props.state.setWorkerId(e.currentTarget.value)}
        onFocus={() => props.state.prefetchOnlineWorkerInfos()}
        onPointerDown={() => props.state.prefetchOnlineWorkerInfos()}
      >
        <Show when={props.state.workers().length === 0}>
          <option value="">No workers online</option>
        </Show>
        <For each={props.state.workers()}>
          {(b) => {
            const info = () => workerInfoStore.workerInfo(b.id)
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
    </div>
  )
}

import type { Accessor, Component } from 'solid-js'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import { createMemo } from 'solid-js'
import { LabeledField } from '~/components/common/LabeledField'
import { LoadingMenu } from '~/components/common/LoadingMenu'
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
  // Warm the per-worker metadata when the menu OPENS.
  //
  // The `<select>` this replaced prefetched on `focus` and `pointerDown`;
  // `onOpen` is the menu's equivalent, and it is deliberately not keyed on the
  // worker list arriving. The prefetch fans out one E2EE handshake per online
  // worker, so firing it whenever the list changes would turn a lazy warm-up
  // into a storm on every refresh.
  //
  // The TRIGGER's own label does not depend on this: `createWorkerDialogContext`
  // fetches the selected worker's info from its own effect, so only the other
  // rows wait for the fan-out.
  // MEMOIZED, for the reason `BranchSelect` states. `workerInfo` reads the
  // whole-map signal, so ONE worker's metadata arriving notifies every reader --
  // and `onOpen` fans out a prefetch per online worker, so replies land
  // continuously while the menu is open. A plain `.map` hands `<For>` a fresh
  // array of fresh objects each time, and `<For>` reconciles by reference, so
  // every row was torn down and rebuilt on each reply: the row under the pointer
  // moved, and a focused row lost focus with the node it sat on.
  const options = createMemo(() => props.state.workers().map((b) => {
    const info = workerInfoStore.workerInfo(b.id)
    if (!info)
      return { value: b.id, label: b.id }
    const details = [info.version, info.os, info.arch].filter(Boolean).join(', ')
    return { value: b.id, label: details ? `${info.name} (${details})` : info.name }
  }))

  return (
    <LabeledField
      label="Worker"
      actions={<RefreshButton onClick={props.state.refreshWorkers} disabled={props.state.workersRefreshing()} title="Refresh workers" />}
    >
      <LoadingMenu
        ariaLabel="Worker"
        value={props.state.workerId()}
        onChange={id => props.state.setWorkerId(id)}
        emptyLabel="No workers online"
        options={options()}
        onOpen={() => props.state.prefetchOnlineWorkerInfos()}
        data-testid="worker-select-menu"
      />
    </LabeledField>
  )
}

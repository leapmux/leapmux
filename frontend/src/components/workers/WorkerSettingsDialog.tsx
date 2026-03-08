import type { Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import * as styles from './WorkerSettingsDialog.css'

interface WorkerSettingsDialogProps {
  worker: Worker
  onClose: () => void
  onDeregistered: () => void
}

export const WorkerSettingsDialog: Component<WorkerSettingsDialogProps> = (props) => {
  const [deregisterLoading, setDeregisterLoading] = createSignal(false)
  const [deregisterError, setDeregisterError] = createSignal<string | null>(null)

  const handleDeregister = async () => {
    setDeregisterLoading(true)
    setDeregisterError(null)
    try {
      await workerClient.deregisterWorker({ workerId: props.worker.id })
      props.onDeregistered()
    }
    catch (e) {
      setDeregisterError(e instanceof Error ? e.message : 'Failed to deregister worker')
      setDeregisterLoading(false)
    }
  }

  return (
    <Dialog title="Deregister Worker" data-testid="worker-settings-dialog" onClose={() => props.onClose()}>
      <div class={styles.description}>
        Are you sure you want to deregister this worker?
      </div>
      <div class={styles.warning} data-testid="deregister-warning">
        This will terminate all active workspaces and terminals on this worker. This action cannot be undone.
      </div>
      <Show when={deregisterError()}>
        <div class={styles.errorText}>{deregisterError()}</div>
      </Show>
      <footer>
        <button class="outline" onClick={() => props.onClose()} data-testid="deregister-cancel">
          Cancel
        </button>
        <button data-variant="danger" onClick={() => handleDeregister()} disabled={deregisterLoading()} data-testid="deregister-confirm">
          <Show when={deregisterLoading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
          {deregisterLoading() ? 'Deregistering...' : 'Deregister'}
        </button>
      </footer>
    </Dialog>
  )
}

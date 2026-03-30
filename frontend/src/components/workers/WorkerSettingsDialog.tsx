import type { Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { spinner } from '~/styles/animations.css'
import * as styles from './WorkerSettingsDialog.css'

interface WorkerSettingsDialogProps {
  worker: Worker
  onClose: () => void
  onDeregistered: () => void
}

export const WorkerSettingsDialog: Component<WorkerSettingsDialogProps> = (props) => {
  const submitting = createLoadingSignal(apiLoadingTimeoutMs())
  const [deregisterError, setDeregisterError] = createSignal<string | null>(null)

  const handleDeregister = async () => {
    submitting.start()
    setDeregisterError(null)
    try {
      await workerClient.deregisterWorker({ workerId: props.worker.id })
      props.onDeregistered()
    }
    catch (e) {
      setDeregisterError(e instanceof Error ? e.message : 'Failed to deregister worker')
      submitting.stop()
    }
  }

  return (
    <Dialog title="Deregister Worker" busy={submitting.loading()} data-testid="worker-settings-dialog" onClose={() => props.onClose()}>
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
        <button class="outline" disabled={submitting.loading()} onClick={() => props.onClose()} data-testid="deregister-cancel">
          Cancel
        </button>
        <button data-variant="danger" onClick={() => handleDeregister()} disabled={submitting.loading()} data-testid="deregister-confirm">
          <Show when={submitting.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
          {submitting.loading() ? 'Deregistering...' : 'Deregister'}
        </button>
      </footer>
    </Dialog>
  )
}

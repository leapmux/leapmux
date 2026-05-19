import type { Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { Dialog } from '~/components/common/Dialog'
import { Spinner } from '~/components/common/Spinner'
import { useDialogSubmit } from '~/hooks/useDialogSubmit'
import { errorText } from '~/styles/shared.css'
import * as styles from './WorkerSettingsDialog.css'

interface WorkerSettingsDialogProps {
  worker: Worker
  onClose: () => void
  onDeregistered: () => void
}

export const WorkerSettingsDialog: Component<WorkerSettingsDialogProps> = (props) => {
  const { submitting, error: deregisterError, run } = useDialogSubmit({
    fallback: 'Failed to deregister worker',
  })

  const handleDeregister = () => {
    void run(async () => {
      await workerClient.deregisterWorker({ workerId: props.worker.id })
      props.onDeregistered()
    })
  }

  return (
    <Dialog title="Deregister worker" busy={submitting.loading()} data-testid="worker-settings-dialog" onClose={() => props.onClose()}>
      <div class={styles.description}>
        Are you sure you want to deregister this worker?
      </div>
      <div class={styles.warning} data-testid="deregister-warning">
        This will terminate all active workspaces and terminals on this worker. This action cannot be undone.
      </div>
      <Show when={deregisterError()}>
        <div class={errorText}>{deregisterError()}</div>
      </Show>
      <footer>
        <button class="outline" disabled={submitting.loading()} onClick={() => props.onClose()} data-testid="deregister-cancel">
          Cancel
        </button>
        <button data-variant="danger" onClick={() => handleDeregister()} disabled={submitting.loading()} data-testid="deregister-confirm">
          <Show when={submitting.loading()}><Spinner /></Show>
          {submitting.loading() ? 'Deregistering...' : 'Deregister'}
        </button>
      </footer>
    </Dialog>
  )
}

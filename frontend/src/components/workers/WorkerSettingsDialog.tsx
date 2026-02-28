import type { Component } from 'solid-js'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, onMount, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { Dialog } from '~/components/common/Dialog'
import { sanitizeName } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import * as styles from './WorkerSettingsDialog.css'

export type WorkerSettingsTab = 'general' | 'deregister'

interface WorkerSettingsDialogProps {
  worker: Worker
  initialTab?: WorkerSettingsTab
  onClose: () => void
  onRenamed: (newName: string) => void
  onDeregistered: () => void
}

export const WorkerSettingsDialog: Component<WorkerSettingsDialogProps> = (props) => {
  // General tab state
  // eslint-disable-next-line solid/reactivity -- intentionally capture initial value
  const [name, setName] = createSignal(props.worker.name)
  const [renameSaving, setRenameSaving] = createSignal(false)
  const [renameError, setRenameError] = createSignal<string | null>(null)

  // Deregister tab state
  const [deregisterLoading, setDeregisterLoading] = createSignal(false)
  const [deregisterError, setDeregisterError] = createSignal<string | null>(null)

  let tabsRef!: HTMLElement

  onMount(() => {
    // Activate the correct tab based on initialTab prop
    if (props.initialTab && props.initialTab !== 'general' && tabsRef) {
      const tabIndex = props.initialTab === 'deregister' ? 1 : 0
      requestAnimationFrame(() => {
        (tabsRef as any).activeIndex = tabIndex
      })
    }
  })

  const handleRename = async () => {
    const { value: sanitized, error } = sanitizeName(name())
    if (error) {
      setRenameError(error)
      return
    }
    setRenameSaving(true)
    setRenameError(null)
    try {
      await workerClient.renameWorker({ workerId: props.worker.id, name: sanitized })
      props.onRenamed(sanitized)
    }
    catch (e) {
      setRenameError(e instanceof Error ? e.message : 'Failed to rename worker')
    }
    finally {
      setRenameSaving(false)
    }
  }

  const handleRenameKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleRename()
    }
  }

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
    <Dialog title="Worker Settings" data-testid="worker-settings-dialog" onClose={() => props.onClose()}>
      <ot-tabs ref={tabsRef}>
        <nav role="tablist">
          <button role="tab">General</button>
          <button role="tab">Deregister</button>
        </nav>

        <div role="tabpanel">
          <div class="vstack gap-4">
            <label>Name</label>
            <input
              ref={(el) => {
                if (!props.initialTab || props.initialTab === 'general') {
                  requestAnimationFrame(() => {
                    el.focus()
                    el.select()
                  })
                }
              }}
              value={name()}
              onInput={e => setName(sanitizeName(e.currentTarget.value).value)}
              onKeyDown={handleRenameKeyDown}
              placeholder="Worker name"
              data-testid="rename-input"
            />
            <Show when={renameError()}>
              <div class={styles.errorText}>{renameError()}</div>
            </Show>
            <footer>
              <button onClick={() => handleRename()} disabled={renameSaving()}>
                <Show when={renameSaving()}><LoaderCircle size={14} class={spinner} /></Show>
                {renameSaving() ? 'Saving...' : 'Save'}
              </button>
            </footer>
          </div>
        </div>

        <div role="tabpanel">
          <div class={styles.description}>
            Are you sure you want to deregister
            {' '}
            <strong>{props.worker.name}</strong>
            {' '}
            (
            {props.worker.hostname}
            )?
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
              <Show when={deregisterLoading()}><LoaderCircle size={14} class={spinner} /></Show>
              {deregisterLoading() ? 'Deregistering...' : 'Deregister'}
            </button>
          </footer>
        </div>
      </ot-tabs>
    </Dialog>
  )
}

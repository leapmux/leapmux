import type { Component } from 'solid-js'
import type { OrgMember } from '~/generated/leapmux/v1/org_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, For, onMount, Show } from 'solid-js'
import { orgClient, workerClient } from '~/api/clients'
import { useOrg } from '~/context/OrgContext'
import { ShareMode } from '~/generated/leapmux/v1/common_pb'
import { validateName } from '~/lib/validate'
import { spinner } from '~/styles/animations.css'
import { dialogStandard } from '~/styles/shared.css'
import * as styles from './WorkerSettingsDialog.css'

export type WorkerSettingsTab = 'general' | 'sharing' | 'deregister'

interface WorkerSettingsDialogProps {
  worker: Worker
  initialTab?: WorkerSettingsTab
  onClose: () => void
  onRenamed: (newName: string) => void
  onShareModeChanged: (newShareMode: ShareMode) => void
  onDeregistered: () => void
}

export const WorkerSettingsDialog: Component<WorkerSettingsDialogProps> = (props) => {
  let dialogRef!: HTMLDialogElement
  const org = useOrg()

  // General tab state
  // eslint-disable-next-line solid/reactivity -- intentionally capture initial value
  const [name, setName] = createSignal(props.worker.name)
  const [renameSaving, setRenameSaving] = createSignal(false)
  const [renameError, setRenameError] = createSignal<string | null>(null)

  // Sharing tab state
  const [shareMode, setShareMode] = createSignal<ShareMode>(ShareMode.PRIVATE)
  const [selectedUserIds, setSelectedUserIds] = createSignal<string[]>([])
  const [members, setMembers] = createSignal<OrgMember[]>([])
  const [sharingLoading, setSharingLoading] = createSignal(true)
  const [sharingSaving, setSharingSaving] = createSignal(false)
  const [sharingError, setSharingError] = createSignal<string | null>(null)

  // Deregister tab state
  const [deregisterLoading, setDeregisterLoading] = createSignal(false)
  const [deregisterError, setDeregisterError] = createSignal<string | null>(null)

  let tabsRef!: HTMLElement

  onMount(async () => {
    dialogRef.showModal()
    // Activate the correct tab based on initialTab prop
    if (props.initialTab && props.initialTab !== 'general' && tabsRef) {
      const tabIndex = props.initialTab === 'sharing' ? 1 : props.initialTab === 'deregister' ? 2 : 0
      requestAnimationFrame(() => {
        (tabsRef as any).activeIndex = tabIndex
      })
    }
    try {
      const [sharesResp, membersResp] = await Promise.all([
        workerClient.listWorkerShares({ workerId: props.worker.id }),
        orgClient.listOrgMembers({ orgId: org.orgId() }),
      ])
      setShareMode(sharesResp.shareMode || ShareMode.PRIVATE)
      setSelectedUserIds(sharesResp.members.map(m => m.userId))
      setMembers(membersResp.members)
    }
    catch (e) {
      setSharingError(e instanceof Error ? e.message : 'Failed to load sharing info')
    }
    finally {
      setSharingLoading(false)
    }
  })

  const handleRename = async () => {
    const trimmed = name().trim()
    const validationError = validateName(trimmed)
    if (validationError) {
      setRenameError(validationError)
      return
    }
    setRenameSaving(true)
    setRenameError(null)
    try {
      await workerClient.renameWorker({ workerId: props.worker.id, name: trimmed })
      props.onRenamed(trimmed)
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

  const toggleUser = (userId: string) => {
    setSelectedUserIds(prev =>
      prev.includes(userId) ? prev.filter(id => id !== userId) : [...prev, userId],
    )
  }

  const handleSaveSharing = async () => {
    setSharingSaving(true)
    setSharingError(null)
    try {
      await workerClient.updateWorkerSharing({
        workerId: props.worker.id,
        shareMode: shareMode(),
        userIds: shareMode() === ShareMode.MEMBERS ? selectedUserIds() : [],
      })
      props.onShareModeChanged(shareMode())
    }
    catch (e) {
      setSharingError(e instanceof Error ? e.message : 'Failed to update sharing')
    }
    finally {
      setSharingSaving(false)
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
    <dialog ref={dialogRef} class={dialogStandard} data-testid="worker-settings-dialog" onClose={() => props.onClose()}>
      <header><h2>Worker Settings</h2></header>

      <ot-tabs ref={tabsRef}>
        <nav role="tablist">
          <button role="tab">General</button>
          <button role="tab">Sharing</button>
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
              onInput={e => setName(e.currentTarget.value)}
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
          <Show when={!sharingLoading()} fallback={<div>Loading...</div>}>
            <fieldset>
              <label>
                <input type="radio" name="shareMode" value={String(ShareMode.PRIVATE)} checked={shareMode() === ShareMode.PRIVATE} onChange={() => setShareMode(ShareMode.PRIVATE)} />
                <span>Private</span>
              </label>
              <label>
                <input type="radio" name="shareMode" value={String(ShareMode.ORG)} checked={shareMode() === ShareMode.ORG} onChange={() => setShareMode(ShareMode.ORG)} />
                <span>All org members</span>
              </label>
              <label>
                <input type="radio" name="shareMode" value={String(ShareMode.MEMBERS)} checked={shareMode() === ShareMode.MEMBERS} onChange={() => setShareMode(ShareMode.MEMBERS)} />
                <span>Specific members</span>
              </label>
            </fieldset>
            <Show when={shareMode() === ShareMode.MEMBERS}>
              <div class={styles.memberList}>
                <For each={members()}>
                  {m => (
                    <label class={styles.memberItem}>
                      <input
                        type="checkbox"
                        checked={selectedUserIds().includes(m.userId)}
                        onChange={() => toggleUser(m.userId)}
                      />
                      <span>
                        {m.displayName || m.username}
                      </span>
                    </label>
                  )}
                </For>
              </div>
            </Show>
            <Show when={sharingError()}>
              <div class={styles.errorText}>{sharingError()}</div>
            </Show>
            <footer>
              <button onClick={() => handleSaveSharing()} disabled={sharingSaving()}>
                <Show when={sharingSaving()}><LoaderCircle size={14} class={spinner} /></Show>
                {sharingSaving() ? 'Saving...' : 'Save'}
              </button>
            </footer>
          </Show>
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
    </dialog>
  )
}

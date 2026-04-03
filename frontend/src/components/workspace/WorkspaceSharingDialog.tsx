import type { Component } from 'solid-js'
import type { OrgMember } from '~/generated/leapmux/v1/org_pb'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createSignal, For, onMount, Show } from 'solid-js'
import { orgClient, workspaceClient } from '~/api/clients'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { Dialog } from '~/components/common/Dialog'
import { Icon } from '~/components/common/Icon'
import { useOrg } from '~/context/OrgContext'
import { ShareMode } from '~/generated/leapmux/v1/common_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { spinner } from '~/styles/animations.css'
import { errorText } from '~/styles/shared.css'
import * as styles from './WorkspaceSharingDialog.css'

interface WorkspaceSharingDialogProps {
  workspaceId: string
  onClose: () => void
  onSaved: () => void
}

export const WorkspaceSharingDialog: Component<WorkspaceSharingDialogProps> = (props) => {
  const org = useOrg()
  const [shareMode, setShareMode] = createSignal<ShareMode>(ShareMode.PRIVATE)
  const [selectedUserIds, setSelectedUserIds] = createSignal<string[]>([])
  const [members, setMembers] = createSignal<OrgMember[]>([])
  const [loading, setLoading] = createSignal(true)
  const saving = createLoadingSignal(apiLoadingTimeoutMs())
  const [error, setError] = createSignal<string | null>(null)

  onMount(async () => {
    try {
      const [sharesResp, membersResp] = await Promise.all([
        workspaceClient.listWorkspaceShares({ workspaceId: props.workspaceId }),
        orgClient.listOrgMembers({ orgId: org.orgId() }),
      ])
      setShareMode(sharesResp.shareMode || ShareMode.PRIVATE)
      setSelectedUserIds(sharesResp.members.map(m => m.userId))
      setMembers(membersResp.members)
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load sharing info')
    }
    finally {
      setLoading(false)
    }
  })

  const toggleUser = (userId: string) => {
    setSelectedUserIds(prev =>
      prev.includes(userId) ? prev.filter(id => id !== userId) : [...prev, userId],
    )
  }

  const handleSave = async () => {
    saving.start()
    setError(null)
    try {
      await workspaceClient.updateWorkspaceSharing({
        workspaceId: props.workspaceId,
        shareMode: shareMode(),
        userIds: shareMode() === ShareMode.MEMBERS ? selectedUserIds() : [],
      })
      props.onSaved()
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update sharing')
    }
    finally {
      saving.stop()
    }
  }

  return (
    <Dialog title="Workspace Sharing" busy={saving.loading()} onClose={() => props.onClose()}>
      <Show when={!loading()} fallback={<div>Loading...</div>}>
        <section>
          <div class="vstack gap-4">
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
            <Show when={error()}>
              <div class={errorText}>{error()}</div>
            </Show>
          </div>
        </section>
        <footer>
          <button class="outline" disabled={saving.loading()} onClick={() => props.onClose()}>
            Cancel
          </button>
          <button onClick={() => handleSave()} disabled={saving.loading()}>
            <Show when={saving.loading()}><Icon icon={LoaderCircle} size="sm" class={spinner} /></Show>
            {saving.loading() ? 'Saving...' : 'Save'}
          </button>
        </footer>
      </Show>
    </Dialog>
  )
}

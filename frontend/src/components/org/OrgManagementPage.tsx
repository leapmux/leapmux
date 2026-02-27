import type { Component } from 'solid-js'
import type { OrgMember } from '~/generated/leapmux/v1/org_pb'

import { A, useParams } from '@solidjs/router'
import { createEffect, createSignal, For, Show } from 'solid-js'
import { orgClient } from '~/api/clients'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { OrgMemberRole } from '~/generated/leapmux/v1/org_pb'
import { sanitizeSlug } from '~/lib/validate'
import * as styles from './OrgManagementPage.css'

export const OrgManagementPage: Component = () => {
  const auth = useAuth()
  const org = useOrg()
  const params = useParams<{ orgSlug: string }>()

  // Org name editing
  const [editName, setEditName] = createSignal('')
  const [savingName, setSavingName] = createSignal(false)
  const [nameError, setNameError] = createSignal<string | null>(null)
  const [nameSuccess, setNameSuccess] = createSignal<string | null>(null)

  // Members
  const [members, setMembers] = createSignal<OrgMember[]>([])
  const [membersLoading, setMembersLoading] = createSignal(false)
  const [membersError, setMembersError] = createSignal<string | null>(null)

  // Invite form
  const [inviteUsername, setInviteUsername] = createSignal('')
  const [inviteRole, setInviteRole] = createSignal<OrgMemberRole>(OrgMemberRole.MEMBER)
  const [inviting, setInviting] = createSignal(false)
  const [inviteError, setInviteError] = createSignal<string | null>(null)
  const [inviteSuccess, setInviteSuccess] = createSignal<string | null>(null)

  // Create org
  const [newOrgName, setNewOrgName] = createSignal('')
  const [creatingOrg, setCreatingOrg] = createSignal(false)
  const [createOrgError, setCreateOrgError] = createSignal<string | null>(null)
  const [createOrgSuccess, setCreateOrgSuccess] = createSignal<string | null>(null)

  // Delete org
  const [deletingOrg, setDeletingOrg] = createSignal(false)
  const [deleteError, setDeleteError] = createSignal<string | null>(null)

  // Confirm dialog state
  const [confirmRemoveMember, setConfirmRemoveMember] = createSignal<{ userId: string, username: string } | null>(null)
  const [confirmDeleteOrg, setConfirmDeleteOrg] = createSignal(false)

  const fetchMembers = async () => {
    const id = org.orgId()
    if (!id)
      return
    setMembersLoading(true)
    setMembersError(null)
    try {
      const resp = await orgClient.listOrgMembers({ orgId: id })
      setMembers(resp.members)
    }
    catch (e) {
      setMembersError(e instanceof Error ? e.message : 'Failed to load members')
    }
    finally {
      setMembersLoading(false)
    }
  }

  // Load org name and members when org changes
  createEffect(() => {
    const current = org.org()
    if (current) {
      setEditName(current.name)
      fetchMembers()
    }
  })

  const handleSaveName = async () => {
    const id = org.orgId()
    if (!id)
      return
    const [slug, slugErr] = sanitizeSlug('Organization name', editName())
    if (slugErr) {
      setNameError(slugErr)
      return
    }
    setSavingName(true)
    setNameError(null)
    setNameSuccess(null)
    try {
      await orgClient.updateOrg({ orgId: id, name: slug })
      setNameSuccess('Organization name updated.')
      await org.refetch()
    }
    catch (e) {
      setNameError(e instanceof Error ? e.message : 'Failed to update name')
    }
    finally {
      setSavingName(false)
    }
  }

  const handleInvite = async () => {
    const id = org.orgId()
    if (!id)
      return
    const [slug, slugErr] = sanitizeSlug('Username', inviteUsername())
    if (slugErr) {
      setInviteError(slugErr)
      return
    }
    setInviting(true)
    setInviteError(null)
    setInviteSuccess(null)
    try {
      await orgClient.inviteOrgMember({
        orgId: id,
        username: slug,
        role: inviteRole(),
      })
      setInviteSuccess(`Invited ${slug} as ${OrgMemberRole[inviteRole()]?.toLowerCase() ?? 'member'}.`)
      setInviteUsername('')
      await fetchMembers()
    }
    catch (e) {
      setInviteError(e instanceof Error ? e.message : 'Failed to invite member')
    }
    finally {
      setInviting(false)
    }
  }

  const handleRemoveMember = (userId: string, username: string) => {
    setConfirmRemoveMember({ userId, username })
  }

  const doRemoveMember = async (userId: string) => {
    const id = org.orgId()
    if (!id)
      return
    try {
      await orgClient.removeOrgMember({ orgId: id, userId })
      await fetchMembers()
    }
    catch (e) {
      setMembersError(e instanceof Error ? e.message : 'Failed to remove member')
    }
  }

  const handleRoleChange = async (userId: string, role: OrgMemberRole) => {
    const id = org.orgId()
    if (!id)
      return
    try {
      await orgClient.updateOrgMember({ orgId: id, userId, role })
      await fetchMembers()
    }
    catch (e) {
      setMembersError(e instanceof Error ? e.message : 'Failed to update role')
    }
  }

  const handleCreateOrg = async () => {
    const [slug, slugErr] = sanitizeSlug('Organization name', newOrgName())
    if (slugErr) {
      setCreateOrgError(slugErr)
      return
    }
    setCreatingOrg(true)
    setCreateOrgError(null)
    setCreateOrgSuccess(null)
    try {
      await orgClient.createOrg({ name: slug })
      setCreateOrgSuccess(`Organization "${slug}" created.`)
      setNewOrgName('')
      await org.refetch()
    }
    catch (e) {
      setCreateOrgError(e instanceof Error ? e.message : 'Failed to create organization')
    }
    finally {
      setCreatingOrg(false)
    }
  }

  const handleDeleteOrg = () => {
    setConfirmDeleteOrg(true)
  }

  const doDeleteOrg = async () => {
    const id = org.orgId()
    if (!id)
      return
    setDeletingOrg(true)
    setDeleteError(null)
    try {
      await orgClient.deleteOrg({ orgId: id })
      await org.refetch()
    }
    catch (e) {
      setDeleteError(e instanceof Error ? e.message : 'Failed to delete organization')
    }
    finally {
      setDeletingOrg(false)
    }
  }

  const isPersonal = () => org.org()?.isPersonal ?? false
  const currentUserId = () => auth.user()?.id ?? ''

  const formatDate = (timestamp: unknown): string => {
    if (!timestamp)
      return '-'
    const ts = timestamp as { seconds?: bigint }
    if (ts.seconds) {
      return new Date(Number(ts.seconds) * 1000).toLocaleDateString()
    }
    return '-'
  }

  return (
    <div class={styles.container}>
      <A href={`/o/${params.orgSlug}`} class={styles.backLink}>&larr; Dashboard</A>
      <h1>Organization Settings</h1>

      {/* Org Info Section */}
      <div class="card mb-6">
        <h2>General</h2>
        <div class={styles.infoRow}>
          <span class={styles.infoLabel}>Name</span>
          <Show
            when={!isPersonal()}
            fallback={<span class={styles.infoValue}>{org.org()?.name ?? ''}</span>}
          >
            <input
              type="text"
              value={editName()}
              onInput={e => setEditName(e.currentTarget.value)}
            />
            <button
              disabled={savingName() || !editName().trim() || editName() === org.org()?.name}
              onClick={() => handleSaveName()}
            >
              {savingName() ? 'Saving...' : 'Save'}
            </button>
          </Show>
        </div>
        <Show when={isPersonal()}>
          <div class={styles.infoRow}>
            <span class={styles.infoLabel}>Type</span>
            <span class={styles.infoValue}>Personal</span>
          </div>
        </Show>
        <Show when={nameError()}>
          <div class={styles.errorText}>{nameError()}</div>
        </Show>
        <Show when={nameSuccess()}>
          <div class={styles.successText}>{nameSuccess()}</div>
        </Show>
      </div>

      {/* Members Section */}
      <div class="card mb-6">
        <h2>Members</h2>

        {/* Invite Form */}
        <div class={styles.inviteForm}>
          <input
            type="text"
            placeholder="Username"
            value={inviteUsername()}
            onInput={e => setInviteUsername(e.currentTarget.value)}
          />
          <select
            value={String(inviteRole())}
            onChange={e => setInviteRole(Number(e.currentTarget.value) as OrgMemberRole)}
          >
            <option value={String(OrgMemberRole.MEMBER)}>Member</option>
            <option value={String(OrgMemberRole.ADMIN)}>Admin</option>
            <option value={String(OrgMemberRole.OWNER)}>Owner</option>
          </select>
          <button
            disabled={inviting() || !inviteUsername().trim()}
            onClick={() => handleInvite()}
          >
            {inviting() ? 'Inviting...' : 'Invite'}
          </button>
        </div>
        <Show when={inviteError()}>
          <div class={styles.errorText}>{inviteError()}</div>
        </Show>
        <Show when={inviteSuccess()}>
          <div class={styles.successText}>{inviteSuccess()}</div>
        </Show>

        {/* Members Table */}
        <Show when={membersLoading()}>
          <div class={styles.emptyState}>Loading members...</div>
        </Show>
        <Show when={membersError()}>
          <div class={styles.errorText}>{membersError()}</div>
        </Show>
        <Show
          when={!membersLoading() && members().length > 0}
          fallback={(
            <Show when={!membersLoading()}>
              <div class={styles.emptyState}>No members found.</div>
            </Show>
          )}
        >
          <table>
            <thead>
              <tr>
                <th>Username</th>
                <th>Display Name</th>
                <th>Role</th>
                <th>Joined</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <For each={members()}>
                {member => (
                  <tr>
                    <td>{member.username}</td>
                    <td>{member.displayName || '-'}</td>
                    <td>
                      <select
                        value={String(member.role)}
                        disabled={member.userId === currentUserId()}
                        onChange={e => handleRoleChange(member.userId, Number(e.currentTarget.value) as OrgMemberRole)}
                      >
                        <option value={String(OrgMemberRole.MEMBER)}>Member</option>
                        <option value={String(OrgMemberRole.ADMIN)}>Admin</option>
                        <option value={String(OrgMemberRole.OWNER)}>Owner</option>
                      </select>
                    </td>
                    <td>{formatDate(member.joinedAt)}</td>
                    <td>
                      <button
                        data-variant="danger"
                        disabled={member.userId === currentUserId()}
                        onClick={() => handleRemoveMember(member.userId, member.username)}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </Show>
      </div>

      {/* Create Org Section */}
      <div class="card mb-6">
        <h2>Create Organization</h2>
        <div class={styles.inviteForm}>
          <input
            type="text"
            placeholder="Organization name"
            value={newOrgName()}
            onInput={e => setNewOrgName(e.currentTarget.value)}
          />
          <button
            disabled={creatingOrg() || !newOrgName().trim()}
            onClick={() => handleCreateOrg()}
          >
            {creatingOrg() ? 'Creating...' : 'Create'}
          </button>
        </div>
        <Show when={createOrgError()}>
          <div class={styles.errorText}>{createOrgError()}</div>
        </Show>
        <Show when={createOrgSuccess()}>
          <div class={styles.successText}>{createOrgSuccess()}</div>
        </Show>
      </div>

      {/* Delete Org Section */}
      <Show when={!isPersonal()}>
        <div class={styles.deleteSection}>
          <h2>Danger Zone</h2>
          <p class={styles.deleteDescription}>
            Permanently delete this organization and all its data. This action cannot be undone.
          </p>
          <button
            data-variant="danger"
            disabled={deletingOrg()}
            onClick={() => handleDeleteOrg()}
          >
            {deletingOrg() ? 'Deleting...' : 'Delete Organization'}
          </button>
          <Show when={deleteError()}>
            <div class={styles.errorText}>{deleteError()}</div>
          </Show>
        </div>
      </Show>

      <Show when={confirmRemoveMember()}>
        {member => (
          <ConfirmDialog
            title="Remove Member"
            confirmLabel="Remove"
            danger
            onConfirm={() => {
              doRemoveMember(member().userId)
              setConfirmRemoveMember(null)
            }}
            onCancel={() => setConfirmRemoveMember(null)}
          >
            <p>
              Remove
              {member().username}
              {' '}
              from this organization?
            </p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={confirmDeleteOrg()}>
        <ConfirmDialog
          title="Delete Organization"
          confirmLabel="Delete"
          danger
          onConfirm={() => {
            setConfirmDeleteOrg(false)
            doDeleteOrg()
          }}
          onCancel={() => setConfirmDeleteOrg(false)}
        >
          <p>
            Are you sure you want to delete "
            {org.org()?.name}
            "? This cannot be undone.
          </p>
        </ConfirmDialog>
      </Show>
    </div>
  )
}

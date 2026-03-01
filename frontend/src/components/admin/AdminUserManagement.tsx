import type { Component } from 'solid-js'
import type { AdminUserView } from '~/generated/leapmux/v1/admin_pb'
import { createSignal, For, onMount, Show } from 'solid-js'
import { adminClient } from '~/api/clients'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { useAuth } from '~/context/AuthContext'
import { sanitizeSlug } from '~/lib/validate'
import * as styles from './AdminSettingsPage.css'

export const AdminUserManagement: Component = () => {
  const auth = useAuth()

  // --- User Management state ---
  const [users, setUsers] = createSignal<AdminUserView[]>([])
  const [userQuery, setUserQuery] = createSignal('')
  const [usersLoading, setUsersLoading] = createSignal(false)
  const [usersError, setUsersError] = createSignal<string | null>(null)
  const [resetPasswordUserId, setResetPasswordUserId] = createSignal<string | null>(null)
  const [resetPasswordValue, setResetPasswordValue] = createSignal('')
  const [resetPasswordMessage, setResetPasswordMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // Confirm dialog state
  const [confirmDeleteUser, setConfirmDeleteUser] = createSignal<AdminUserView | null>(null)

  // --- Create User state ---
  const [newUsername, setNewUsername] = createSignal('')
  const [newPassword, setNewPassword] = createSignal('')
  const [newDisplayName, setNewDisplayName] = createSignal('')
  const [newEmail, setNewEmail] = createSignal('')
  const [newIsAdmin, setNewIsAdmin] = createSignal(false)
  const [createUserSaving, setCreateUserSaving] = createSignal(false)
  const [createUserMessage, setCreateUserMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // --- Load users ---
  const loadUsers = async () => {
    setUsersLoading(true)
    setUsersError(null)
    try {
      const resp = await adminClient.listUsers({ query: userQuery() })
      setUsers(resp.users ?? [])
    }
    catch (e) {
      setUsersError(e instanceof Error ? e.message : 'Failed to load users')
    }
    finally {
      setUsersLoading(false)
    }
  }

  onMount(() => {
    loadUsers()
  })

  // --- User actions ---
  const handleToggleAdmin = async (user: AdminUserView) => {
    try {
      await adminClient.updateUser({
        userId: user.id,
        displayName: user.displayName,
        email: user.email,
        isAdmin: !user.isAdmin,
      })
      await loadUsers()
    }
    catch (e) {
      setUsersError(e instanceof Error ? e.message : 'Failed to update user')
    }
  }

  const handleDeleteUser = (user: AdminUserView) => {
    setConfirmDeleteUser(user)
  }

  const doDeleteUser = async (userId: string) => {
    try {
      await adminClient.deleteUser({ userId })
      await loadUsers()
    }
    catch (e) {
      setUsersError(e instanceof Error ? e.message : 'Failed to delete user')
    }
  }

  const handleResetPassword = async (userId: string) => {
    if (!resetPasswordValue()) {
      return
    }
    setResetPasswordMessage(null)
    try {
      await adminClient.resetUserPassword({ userId, newPassword: resetPasswordValue() })
      setResetPasswordMessage({ type: 'success', text: 'Password reset.' })
      setResetPasswordUserId(null)
      setResetPasswordValue('')
    }
    catch (e) {
      setResetPasswordMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to reset password' })
    }
  }

  // --- Create user ---
  const handleCreateUser = async () => {
    const [slug, slugErr] = sanitizeSlug('Username', newUsername())
    if (slugErr) {
      setCreateUserMessage({ type: 'error', text: slugErr })
      return
    }
    setCreateUserSaving(true)
    setCreateUserMessage(null)
    try {
      await adminClient.createUser({
        username: slug,
        password: newPassword(),
        displayName: newDisplayName(),
        email: newEmail(),
        isAdmin: newIsAdmin(),
      })
      setCreateUserMessage({ type: 'success', text: `User "${slug}" created.` })
      setNewUsername('')
      setNewPassword('')
      setNewDisplayName('')
      setNewEmail('')
      setNewIsAdmin(false)
      await loadUsers()
    }
    catch (e) {
      setCreateUserMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to create user' })
    }
    finally {
      setCreateUserSaving(false)
    }
  }

  // --- Search handler ---
  const handleSearch = () => {
    loadUsers()
  }

  return (
    <>
      {/* ===== User Management ===== */}
      <div class={styles.section}>
        <h2>User Management</h2>

        <div class={styles.searchRow}>
          <input
            type="text"
            placeholder="Search users..."
            value={userQuery()}
            onInput={e => setUserQuery(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter')
                handleSearch()
            }}
          />
          <button data-variant="secondary" onClick={handleSearch}>
            Search
          </button>
        </div>

        <Show when={usersError()}>
          <div role="alert" class={styles.errorText}>{usersError()}</div>
        </Show>

        <Show when={resetPasswordMessage()}>
          {msg => (
            <div role="alert" class={msg().type === 'success' ? styles.successText : styles.errorText}>
              {msg().text}
            </div>
          )}
        </Show>

        <Show when={usersLoading()}>
          <div class={styles.mutedHint}>
            Loading users...
          </div>
        </Show>

        <Show when={!usersLoading() && users().length > 0}>
          <table class={styles.autoTable}>
            <thead>
              <tr>
                <th>Username</th>
                <th>Display Name</th>
                <th>Email</th>
                <th>Role</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <For each={users()}>
                {user => (
                  <tr>
                    <td>{user.username}</td>
                    <td>{user.displayName}</td>
                    <td>{user.email}</td>
                    <td>
                      <Show when={user.isAdmin}>
                        <span class="badge secondary">Admin</span>
                      </Show>
                    </td>
                    <td>
                      <div class={`hstack gap-1 ${styles.flexWrap}`}>
                        <Show when={user.id !== auth.user()?.id}>
                          <button
                            class="small outline"
                            onClick={() => handleToggleAdmin(user)}
                          >
                            {user.isAdmin ? 'Revoke Admin' : 'Make Admin'}
                          </button>
                        </Show>
                        <Show
                          when={resetPasswordUserId() === user.id}
                          fallback={(
                            <button
                              class="small outline"
                              onClick={() => {
                                setResetPasswordUserId(user.id)
                                setResetPasswordValue('')
                                setResetPasswordMessage(null)
                              }}
                            >
                              Reset Password
                            </button>
                          )}
                        >
                          <div class={styles.inlineResetRow}>
                            <input
                              class={styles.inlineInput}
                              type="password"
                              placeholder="New password"
                              value={resetPasswordValue()}
                              onInput={e => setResetPasswordValue(e.currentTarget.value)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter')
                                  handleResetPassword(user.id)
                              }}
                            />
                            <button
                              class="small outline"
                              onClick={() => handleResetPassword(user.id)}
                              disabled={!resetPasswordValue()}
                            >
                              Set
                            </button>
                            <button
                              class="small outline"
                              onClick={() => {
                                setResetPasswordUserId(null)
                                setResetPasswordValue('')
                              }}
                            >
                              Cancel
                            </button>
                          </div>
                        </Show>
                        <Show when={user.id !== auth.user()?.id}>
                          <button
                            class="small outline"
                            data-variant="danger"
                            onClick={() => handleDeleteUser(user)}
                          >
                            Delete
                          </button>
                        </Show>
                      </div>
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </Show>

        <Show when={!usersLoading() && users().length === 0 && !usersError()}>
          <div class={styles.mutedHint}>
            No users found.
          </div>
        </Show>
      </div>

      {/* ===== Create User ===== */}
      <div class={styles.section}>
        <h2>Create User</h2>
        <div class="vstack gap-4">
          <label class={styles.fieldLabel}>
            Username
            <input value={newUsername()} onInput={e => setNewUsername(e.currentTarget.value)} />
          </label>
          <label class={styles.fieldLabel}>
            Password
            <input type="password" value={newPassword()} onInput={e => setNewPassword(e.currentTarget.value)} />
          </label>
          <label class={styles.fieldLabel}>
            Display Name
            <input value={newDisplayName()} onInput={e => setNewDisplayName(e.currentTarget.value)} />
          </label>
          <label class={styles.fieldLabel}>
            Email
            <input type="email" value={newEmail()} onInput={e => setNewEmail(e.currentTarget.value)} />
          </label>
          <label>
            <input type="checkbox" checked={newIsAdmin()} onChange={e => setNewIsAdmin(e.currentTarget.checked)} />
            Admin
          </label>

          <Show when={createUserMessage()}>
            {msg => (
              <div role="alert" class={msg().type === 'success' ? styles.successText : styles.errorText}>
                {msg().text}
              </div>
            )}
          </Show>

          <button
            onClick={handleCreateUser}
            disabled={createUserSaving() || !newUsername() || !newPassword()}
          >
            {createUserSaving() ? 'Creating...' : 'Create User'}
          </button>
        </div>
      </div>

      <Show when={confirmDeleteUser()}>
        {user => (
          <ConfirmDialog
            title="Delete User"
            confirmLabel="Delete"
            danger
            onConfirm={() => {
              doDeleteUser(user().id)
              setConfirmDeleteUser(null)
            }}
            onCancel={() => setConfirmDeleteUser(null)}
          >
            <p>
              Delete user "
              {user().username}
              "? This cannot be undone.
            </p>
          </ConfirmDialog>
        )}
      </Show>
    </>
  )
}

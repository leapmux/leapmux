import type { Component } from 'solid-js'
import type { AdminUserView } from '~/generated/leapmux/v1/admin_pb'
import { A } from '@solidjs/router'
import { createSignal, For, onMount, Show } from 'solid-js'
import { adminClient } from '~/api/clients'
import { useAuth } from '~/context/AuthContext'
import * as styles from './AdminSettingsPage.css'

export const AdminSettingsPage: Component = () => {
  const auth = useAuth()

  // --- System Settings state ---
  const [signupEnabled, setSignupEnabled] = createSignal(false)
  const [emailVerificationRequired, setEmailVerificationRequired] = createSignal(false)
  const [smtpHost, setSmtpHost] = createSignal('')
  const [smtpPort, setSmtpPort] = createSignal(587)
  const [smtpUsername, setSmtpUsername] = createSignal('')
  const [smtpPassword, setSmtpPassword] = createSignal('')
  const [smtpPasswordSet, setSmtpPasswordSet] = createSignal(false)
  const [smtpFromAddress, setSmtpFromAddress] = createSignal('')
  const [smtpUseTls, setSmtpUseTls] = createSignal(true)
  const [settingsSaving, setSettingsSaving] = createSignal(false)
  const [settingsMessage, setSettingsMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // --- User Management state ---
  const [users, setUsers] = createSignal<AdminUserView[]>([])
  const [userQuery, setUserQuery] = createSignal('')
  const [usersLoading, setUsersLoading] = createSignal(false)
  const [usersError, setUsersError] = createSignal<string | null>(null)
  const [resetPasswordUserId, setResetPasswordUserId] = createSignal<string | null>(null)
  const [resetPasswordValue, setResetPasswordValue] = createSignal('')
  const [resetPasswordMessage, setResetPasswordMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // --- Log Level state ---
  const logLevels = ['DEBUG', 'INFO', 'WARN', 'ERROR']
  const [logLevel, setLogLevel] = createSignal('INFO')
  const [logLevelMessage, setLogLevelMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const loadLogLevel = async () => {
    try {
      const resp = await adminClient.getLogLevel({})
      setLogLevel(resp.level || 'INFO')
    }
    catch (e) {
      setLogLevelMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to load log level' })
    }
  }

  const handleSetLogLevel = async (level: string) => {
    setLogLevelMessage(null)
    try {
      const resp = await adminClient.setLogLevel({ level })
      setLogLevel(resp.level || level)
      setLogLevelMessage({ type: 'success', text: `Log level set to ${resp.level}.` })
    }
    catch (e) {
      setLogLevelMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to set log level' })
    }
  }

  // --- Create User state ---
  const [newUsername, setNewUsername] = createSignal('')
  const [newPassword, setNewPassword] = createSignal('')
  const [newDisplayName, setNewDisplayName] = createSignal('')
  const [newEmail, setNewEmail] = createSignal('')
  const [newIsAdmin, setNewIsAdmin] = createSignal(false)
  const [createUserSaving, setCreateUserSaving] = createSignal(false)
  const [createUserMessage, setCreateUserMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  // --- Load settings ---
  const loadSettings = async () => {
    try {
      const resp = await adminClient.getSettings({})
      const s = resp.settings
      if (s) {
        setSignupEnabled(s.signupEnabled)
        setEmailVerificationRequired(s.emailVerificationRequired)
        if (s.smtp) {
          setSmtpHost(s.smtp.host)
          setSmtpPort(s.smtp.port)
          setSmtpUsername(s.smtp.username)
          setSmtpPasswordSet(s.smtp.passwordSet)
          setSmtpFromAddress(s.smtp.fromAddress)
          setSmtpUseTls(s.smtp.useTls)
        }
      }
    }
    catch (e) {
      setSettingsMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to load settings' })
    }
  }

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
    loadSettings()
    loadLogLevel()
    loadUsers()
  })

  // --- Save settings ---
  const handleSaveSettings = async () => {
    setSettingsSaving(true)
    setSettingsMessage(null)
    try {
      await adminClient.updateSettings({
        settings: {
          signupEnabled: signupEnabled(),
          emailVerificationRequired: emailVerificationRequired(),
          smtp: {
            host: smtpHost(),
            port: smtpPort(),
            username: smtpUsername(),
            password: smtpPassword(),
            passwordSet: smtpPasswordSet(),
            fromAddress: smtpFromAddress(),
            useTls: smtpUseTls(),
          },
        },
      })
      setSettingsMessage({ type: 'success', text: 'Settings saved.' })
      // Clear password field after save and refresh password-set status
      setSmtpPassword('')
      if (smtpPassword()) {
        setSmtpPasswordSet(true)
      }
    }
    catch (e) {
      setSettingsMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to save settings' })
    }
    finally {
      setSettingsSaving(false)
    }
  }

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

  const handleDeleteUser = async (user: AdminUserView) => {
    // eslint-disable-next-line no-alert
    if (!confirm(`Delete user "${user.username}"? This cannot be undone.`)) {
      return
    }
    try {
      await adminClient.deleteUser({ userId: user.id })
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
    setCreateUserSaving(true)
    setCreateUserMessage(null)
    try {
      await adminClient.createUser({
        username: newUsername(),
        password: newPassword(),
        displayName: newDisplayName(),
        email: newEmail(),
        isAdmin: newIsAdmin(),
      })
      setCreateUserMessage({ type: 'success', text: `User "${newUsername()}" created.` })
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
    <div class={styles.pageContainer}>
      <A href={`/o/${auth.user()?.username || ''}`} class={styles.backLink}>&larr; Dashboard</A>
      <h1>Administration</h1>

      {/* ===== System Settings ===== */}
      <div class={styles.section}>
        <h2>System Settings</h2>
        <div class="vstack gap-4">
          <label class={styles.toggleRow}>
            <span class={styles.toggleLabel}>Sign-up enabled</span>
            <input type="checkbox" role="switch" checked={signupEnabled()} onChange={e => setSignupEnabled(e.currentTarget.checked)} />
          </label>

          <Show when={signupEnabled()}>
            <label class={styles.toggleRow}>
              <span class={styles.toggleLabel}>Email verification required</span>
              <input type="checkbox" role="switch" checked={emailVerificationRequired()} onChange={e => setEmailVerificationRequired(e.currentTarget.checked)} />
            </label>
          </Show>

          <div class={styles.subsection}>
            <h3>SMTP Configuration</h3>
            <div class="vstack gap-4">
              <label class={styles.fieldLabel}>
                Host
                <input value={smtpHost()} onInput={e => setSmtpHost(e.currentTarget.value)} placeholder="smtp.example.com" />
              </label>
              <label class={styles.fieldLabel}>
                Port
                <input type="number" value={String(smtpPort())} onInput={e => setSmtpPort(Number.parseInt(e.currentTarget.value) || 0)} />
              </label>
              <label class={styles.fieldLabel}>
                Username
                <input value={smtpUsername()} onInput={e => setSmtpUsername(e.currentTarget.value)} />
              </label>
              <label class={styles.fieldLabel}>
                Password
                <Show when={smtpPasswordSet()}>
                  <span class={styles.passwordSetIndicator}>Password set</span>
                </Show>
                <input
                  type="password"
                  value={smtpPassword()}
                  onInput={e => setSmtpPassword(e.currentTarget.value)}
                  placeholder={smtpPasswordSet() ? 'Leave blank to keep current' : 'Enter password'}
                />
              </label>
              <label class={styles.fieldLabel}>
                From Address
                <input type="email" value={smtpFromAddress()} onInput={e => setSmtpFromAddress(e.currentTarget.value)} placeholder="noreply@example.com" />
              </label>
              <label class={styles.toggleRow}>
                <span class={styles.toggleLabel}>Use TLS</span>
                <input type="checkbox" role="switch" checked={smtpUseTls()} onChange={e => setSmtpUseTls(e.currentTarget.checked)} />
              </label>
            </div>
          </div>

          <Show when={settingsMessage()}>
            {msg => (
              <div role="alert" class={msg().type === 'success' ? styles.successText : styles.errorText}>
                {msg().text}
              </div>
            )}
          </Show>

          <button
            onClick={handleSaveSettings}
            disabled={settingsSaving()}
          >
            {settingsSaving() ? 'Saving...' : 'Save Settings'}
          </button>
        </div>
      </div>

      {/* ===== Log Level ===== */}
      <div class={styles.section}>
        <h2>Hub Log Level</h2>
        <div class="vstack gap-4">
          <div>
            <label class={styles.toggleLabel}>Level</label>
            <select
              value={logLevel()}
              onChange={(e) => {
                const v = e.currentTarget.value
                if (v)
                  handleSetLogLevel(v)
              }}
            >
              <For each={logLevels}>
                {level => <option value={level}>{level}</option>}
              </For>
            </select>
          </div>

          <Show when={logLevelMessage()}>
            {msg => (
              <div role="alert" class={msg().type === 'success' ? styles.successText : styles.errorText}>
                {msg().text}
              </div>
            )}
          </Show>
        </div>
      </div>

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
    </div>
  )
}

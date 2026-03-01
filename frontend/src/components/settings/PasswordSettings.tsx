import type { Component } from 'solid-js'
import { createSignal, Show } from 'solid-js'

import * as styles from './PreferencesPage.css'

export const PasswordSettings: Component = () => {
  const [currentPassword, setCurrentPassword] = createSignal('')
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [passwordSaving, setPasswordSaving] = createSignal(false)
  const [passwordMessage, setPasswordMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const handleChangePassword = async () => {
    if (newPassword() !== confirmPassword()) {
      setPasswordMessage({ type: 'error', text: 'Passwords do not match.' })
      return
    }
    setPasswordSaving(true)
    setPasswordMessage(null)
    try {
      const { changePassword } = await import('~/api/clients').then(m => ({ changePassword: m.userClient.changePassword }))
      await changePassword({
        currentPassword: currentPassword(),
        newPassword: newPassword(),
      })
      setPasswordMessage({ type: 'success', text: 'Password changed.' })
      setCurrentPassword('')
      setNewPassword('')
      setConfirmPassword('')
    }
    catch (e) {
      setPasswordMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to change password' })
    }
    finally {
      setPasswordSaving(false)
    }
  }

  return (
    <div class={styles.section}>
      <h2>Change Password</h2>
      <div class="vstack gap-4">
        <label class={styles.fieldLabel}>
          Current Password
          <input type="password" value={currentPassword()} onInput={e => setCurrentPassword(e.currentTarget.value)} />
        </label>
        <label class={styles.fieldLabel}>
          New Password
          <input type="password" value={newPassword()} onInput={e => setNewPassword(e.currentTarget.value)} />
        </label>
        <label class={styles.fieldLabel}>
          Confirm New Password
          <input type="password" value={confirmPassword()} onInput={e => setConfirmPassword(e.currentTarget.value)} />
        </label>
        <Show when={passwordMessage()}>
          {msg => <div class={msg().type === 'success' ? styles.successText : styles.errorText}>{msg().text}</div>}
        </Show>
        <button onClick={handleChangePassword} disabled={passwordSaving() || !currentPassword() || !newPassword()}>
          {passwordSaving() ? 'Changing...' : 'Change Password'}
        </button>
      </div>
    </div>
  )
}

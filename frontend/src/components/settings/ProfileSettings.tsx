import type { Component } from 'solid-js'
import { createSignal, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { useAuth } from '~/context/AuthContext'
import { sanitizeSlug } from '~/lib/validate'
import { errorText, successText, warningText } from '~/styles/shared.css'
import * as styles from './PreferencesDialog.css'

export const ProfileSettings: Component = () => {
  const auth = useAuth()

  const [username, setUsername] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [profileSaving, setProfileSaving] = createSignal(false)
  const [profileMessage, setProfileMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const [newEmail, setNewEmail] = createSignal('')
  const [emailSaving, setEmailSaving] = createSignal(false)
  const [emailMessage, setEmailMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  onMount(() => {
    const user = auth.user()
    if (user) {
      setUsername(user.username)
      setDisplayName(user.displayName)
    }
  })

  const handleSaveProfile = async () => {
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setProfileMessage({ type: 'error', text: slugErr })
      return
    }
    setProfileSaving(true)
    setProfileMessage(null)
    try {
      await userClient.updateProfile({
        username: slug,
        displayName: displayName(),
      })
      setProfileMessage({ type: 'success', text: 'Profile updated.' })
    }
    catch (e) {
      setProfileMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to update profile' })
    }
    finally {
      setProfileSaving(false)
    }
  }

  const handleRequestEmailChange = async () => {
    const email = newEmail().trim()
    if (!email) {
      setEmailMessage({ type: 'error', text: 'Email must not be empty.' })
      return
    }
    setEmailSaving(true)
    setEmailMessage(null)
    try {
      const resp = await userClient.requestEmailChange({ newEmail: email })
      if (resp.verificationRequired) {
        setEmailMessage({ type: 'success', text: 'Verification email sent. Check your inbox.' })
      }
      else {
        setEmailMessage({ type: 'success', text: 'Email updated.' })
      }
      setNewEmail('')
    }
    catch (e) {
      setEmailMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to request email change' })
    }
    finally {
      setEmailSaving(false)
    }
  }

  return (
    <div class={styles.section}>
      <h2>Profile</h2>
      <div class="vstack gap-4">
        <label class={styles.fieldLabel}>
          Username
          <input type="text" value={username()} onInput={e => setUsername(e.currentTarget.value)} />
        </label>
        <Show when={username() !== auth.user()?.username}>
          <div class={warningText}>Changing your username will also rename your personal organization.</div>
        </Show>
        <label class={styles.fieldLabel}>
          Display Name
          <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
        </label>
        <Show when={profileMessage()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
        <button onClick={handleSaveProfile} disabled={profileSaving()}>
          {profileSaving() ? 'Saving...' : 'Save Profile'}
        </button>
      </div>

      <h2 style={{ 'margin-top': 'var(--space-6)' }}>Email</h2>
      <div class="vstack gap-4">
        <label class={styles.fieldLabel}>
          Current Email
          <div style={{ 'font-size': 'var(--text-6)', 'color': 'var(--foreground)' }}>
            {auth.user()?.email || 'Not set'}
            <Show when={auth.user()?.email}>
              <span style={{ 'margin-left': 'var(--space-2)', 'font-size': 'var(--text-8)', 'color': 'var(--success)' }}>
                (verified)
              </span>
            </Show>
          </div>
        </label>
        <Show when={auth.user()?.pendingEmail}>
          <div class={warningText}>
            Pending email change to
            {' '}
            <strong>{auth.user()?.pendingEmail}</strong>
            {' '}
            — check your inbox to verify.
          </div>
        </Show>
        <label class={styles.fieldLabel}>
          New Email
          <input
            type="email"
            value={newEmail()}
            onInput={e => setNewEmail(e.currentTarget.value)}
            placeholder="Enter new email address"
          />
        </label>
        <Show when={emailMessage()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
        <button onClick={handleRequestEmailChange} disabled={emailSaving() || !newEmail().trim()}>
          {emailSaving() ? 'Requesting...' : 'Change Email'}
        </button>
      </div>
    </div>
  )
}

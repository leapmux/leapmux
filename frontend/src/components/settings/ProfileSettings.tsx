import type { Component } from 'solid-js'
import { createSignal, For, onMount, Show } from 'solid-js'
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

  const [currentPassword, setCurrentPassword] = createSignal('')
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [passwordSaving, setPasswordSaving] = createSignal(false)
  const [passwordMessage, setPasswordMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const [unlinkingProvider, setUnlinkingProvider] = createSignal<string | null>(null)

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

  const handleChangePassword = async () => {
    if (newPassword() !== confirmPassword()) {
      setPasswordMessage({ type: 'error', text: 'Passwords do not match.' })
      return
    }
    if (!newPassword()) {
      setPasswordMessage({ type: 'error', text: 'New password is required.' })
      return
    }
    setPasswordSaving(true)
    setPasswordMessage(null)
    try {
      await userClient.changePassword({
        currentPassword: currentPassword(),
        newPassword: newPassword(),
      })
      setPasswordMessage({ type: 'success', text: auth.user()?.passwordSet ? 'Password changed.' : 'Password set.' })
      setCurrentPassword('')
      setNewPassword('')
      setConfirmPassword('')
      auth.refreshUser()
    }
    catch (e) {
      setPasswordMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to change password' })
    }
    finally {
      setPasswordSaving(false)
    }
  }

  const handleUnlink = async (providerName: string) => {
    setUnlinkingProvider(providerName)
    try {
      await userClient.unlinkOAuthProvider({ providerName })
      auth.refreshUser()
    }
    catch (e) {
      setPasswordMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to unlink provider' })
    }
    finally {
      setUnlinkingProvider(null)
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

      <h2 class={styles.sectionHeading}>Email</h2>
      <div class="vstack gap-4">
        <label class={styles.fieldLabel}>
          Current Email
          <div class={styles.emailValue}>
            {auth.user()?.email || 'Not set'}
            <Show when={auth.user()?.email && auth.user()?.emailVerified}>
              <span class={styles.verifiedBadge}>(verified)</span>
            </Show>
            <Show when={auth.user()?.email && !auth.user()?.emailVerified}>
              <span class={styles.unverifiedBadge}>(unverified)</span>
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

      <h2 class={styles.sectionHeading}>Password</h2>
      <div class="vstack gap-4">
        <Show when={auth.user()?.passwordSet}>
          <label class={styles.fieldLabel}>
            Current Password
            <input
              type="password"
              value={currentPassword()}
              onInput={e => setCurrentPassword(e.currentTarget.value)}
              autocomplete="current-password"
            />
          </label>
        </Show>
        <label class={styles.fieldLabel}>
          New Password
          <input
            type="password"
            value={newPassword()}
            onInput={e => setNewPassword(e.currentTarget.value)}
            autocomplete="new-password"
          />
        </label>
        <label class={styles.fieldLabel}>
          Confirm New Password
          <input
            type="password"
            value={confirmPassword()}
            onInput={e => setConfirmPassword(e.currentTarget.value)}
            autocomplete="new-password"
          />
        </label>
        <Show when={passwordMessage()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
        <button onClick={handleChangePassword} disabled={passwordSaving() || !newPassword()}>
          {passwordSaving()
            ? (auth.user()?.passwordSet ? 'Changing...' : 'Setting...')
            : (auth.user()?.passwordSet ? 'Change Password' : 'Set Password')}
        </button>
      </div>

      <Show when={auth.user()?.oauthProviders && auth.user()!.oauthProviders.length > 0}>
        <h2 class={styles.sectionHeading}>Linked Accounts</h2>
        <div class="vstack gap-2">
          <For each={auth.user()?.oauthProviders}>
            {name => (
              <div class={styles.linkedAccount}>
                <span class={styles.linkedAccountName}>{name}</span>
                <button
                  class={styles.linkedAccountUnlink}
                  onClick={() => handleUnlink(name)}
                  disabled={unlinkingProvider() === name}
                >
                  {unlinkingProvider() === name ? 'Unlinking...' : 'Unlink'}
                </button>
              </div>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

import type { Component } from 'solid-js'
import { createMemo, createSignal, For, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { UsernameField } from '~/components/common/UsernameField'
import { useAuth } from '~/context/AuthContext'
import { sanitizeDisplayName, sanitizeName, sanitizeSlug, validateEmail } from '~/lib/validate'
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
  const [unlinkMessage, setUnlinkMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const profileDirty = createMemo(() => {
    const user = auth.user()
    if (!user)
      return false
    return username() !== user.username || displayName() !== user.displayName
  })

  const displayNameError = createMemo(() => {
    const dn = displayName()
    if (!dn)
      return null // empty is allowed (falls back to username)
    return sanitizeName(dn).error
  })

  const emailSameAsCurrent = createMemo(() => {
    const trimmed = newEmail().trim().toLowerCase()
    return trimmed !== '' && trimmed === (auth.user()?.email ?? '').toLowerCase()
  })

  const pwProps = {
    password: newPassword,
    confirmPassword,
    currentPassword,
    get showCurrentPassword() { return !!auth.user()?.passwordSet },
  }

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
    const { value: sanitizedDisplayName, error: dnErr } = sanitizeDisplayName(displayName(), slug)
    if (dnErr) {
      setProfileMessage({ type: 'error', text: dnErr })
      return
    }
    setProfileSaving(true)
    setProfileMessage(null)
    try {
      await userClient.updateProfile({
        username: slug,
        displayName: sanitizedDisplayName,
      })
      await auth.refreshUser()
      setDisplayName(auth.user()?.displayName ?? '')
      setUsername(auth.user()?.username ?? '')
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
    const emailErr = validateEmail(email)
    if (emailErr) {
      setEmailMessage({ type: 'error', text: emailErr })
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
      auth.refreshUser()
    }
    catch (e) {
      setEmailMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to request email change' })
    }
    finally {
      setEmailSaving(false)
    }
  }

  const handleChangePassword = async () => {
    if (!passwordCanSubmit(pwProps))
      return
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

  const handleUnlink = async (providerId: string) => {
    setUnlinkingProvider(providerId)
    setUnlinkMessage(null)
    try {
      await userClient.unlinkOAuthProvider({ providerId })
      auth.refreshUser()
    }
    catch (e) {
      setUnlinkMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to unlink provider' })
    }
    finally {
      setUnlinkingProvider(null)
    }
  }

  return (
    <div class={styles.section}>
      <div class="vstack gap-4">
        <UsernameField value={username} onInput={setUsername} labelClass={styles.fieldLabel} />
        <Show when={username() !== auth.user()?.username}>
          <div class={warningText}>Changing your username will also rename your personal organization.</div>
        </Show>
        <label class={styles.fieldLabel}>
          Display Name
          <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
        </label>
        <Show when={displayNameError()}>
          {err => <div class={errorText}>{err()}</div>}
        </Show>
        <Show when={profileMessage()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
        <button type="button" onClick={handleSaveProfile} disabled={profileSaving() || !profileDirty() || !!displayNameError()}>
          {profileSaving() ? 'Saving...' : 'Save Profile'}
        </button>
      </div>

      <h3>Email</h3>
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
        <Show when={emailSameAsCurrent()}>
          <div class={errorText}>This is already your current email.</div>
        </Show>
        <button type="button" onClick={handleRequestEmailChange} disabled={emailSaving() || !newEmail().trim() || emailSameAsCurrent()}>
          {emailSaving() ? 'Requesting...' : 'Change Email'}
        </button>
      </div>

      <h3 class={styles.sectionHeading}>Password</h3>
      <div class="vstack gap-4">
        <PasswordFields
          password={newPassword}
          setPassword={setNewPassword}
          confirmPassword={confirmPassword}
          setConfirmPassword={setConfirmPassword}
          showCurrentPassword={!!auth.user()?.passwordSet}
          currentPassword={currentPassword}
          setCurrentPassword={setCurrentPassword}
          labelClass={styles.fieldLabel}
        />
        <Show when={passwordMessage()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
        <button type="button" onClick={handleChangePassword} disabled={passwordSaving() || !passwordCanSubmit(pwProps)}>
          {passwordSaving()
            ? (auth.user()?.passwordSet ? 'Changing...' : 'Setting...')
            : (auth.user()?.passwordSet ? 'Change Password' : 'Set Password')}
        </button>
      </div>

      <Show when={auth.user()?.oauthProviders && auth.user()!.oauthProviders.length > 0}>
        <h3 class={styles.sectionHeading}>Linked Accounts</h3>
        <div class="vstack gap-2">
          <For each={auth.user()?.oauthProviders}>
            {provider => (
              <div class={styles.linkedAccount}>
                <span class={styles.linkedAccountName}>{provider.name}</span>
                <button
                  type="button"
                  class={styles.linkedAccountUnlink}
                  onClick={() => handleUnlink(provider.id)}
                  disabled={unlinkingProvider() === provider.id}
                >
                  {unlinkingProvider() === provider.id ? 'Unlinking...' : 'Unlink'}
                </button>
              </div>
            )}
          </For>
        </div>
        <Show when={unlinkMessage()}>
          {msg => <div class={msg().type === 'success' ? successText : errorText}>{msg().text}</div>}
        </Show>
      </Show>
    </div>
  )
}

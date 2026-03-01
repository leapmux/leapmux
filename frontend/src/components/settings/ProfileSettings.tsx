import type { Component } from 'solid-js'
import { createSignal, onMount, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { sanitizeSlug } from '~/lib/validate'
import * as styles from './PreferencesPage.css'

export const ProfileSettings: Component = () => {
  const auth = useAuth()

  const [username, setUsername] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [email, setEmail] = createSignal('')
  const [profileSaving, setProfileSaving] = createSignal(false)
  const [profileMessage, setProfileMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  onMount(() => {
    const user = auth.user()
    if (user) {
      setUsername(user.username)
      setDisplayName(user.displayName)
      setEmail(user.email)
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
      const { updateProfile } = await import('~/api/clients').then(m => ({ updateProfile: m.userClient.updateProfile }))
      await updateProfile({
        username: slug,
        displayName: displayName(),
        email: email(),
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

  return (
    <div class={styles.section}>
      <h2>Profile</h2>
      <div class="vstack gap-4">
        <label class={styles.fieldLabel}>
          Username
          <input type="text" value={username()} onInput={e => setUsername(e.currentTarget.value)} />
        </label>
        <Show when={username() !== auth.user()?.username}>
          <div class={styles.warningText}>Changing your username will also rename your personal organization.</div>
        </Show>
        <label class={styles.fieldLabel}>
          Display Name
          <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
        </label>
        <label class={styles.fieldLabel}>
          Email
          <input type="email" value={email()} onInput={e => setEmail(e.currentTarget.value)} />
        </label>
        <Show when={profileMessage()}>
          {msg => <div class={msg().type === 'success' ? styles.successText : styles.errorText}>{msg().text}</div>}
        </Show>
        <button onClick={handleSaveProfile} disabled={profileSaving()}>
          {profileSaving() ? 'Saving...' : 'Save Profile'}
        </button>
      </div>
    </div>
  )
}

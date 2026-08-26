import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import { createMemo, createSignal, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { StatusLine } from '~/components/common/StatusLine'
import { UsernameField } from '~/components/common/UsernameField'
import { useAuth } from '~/context/AuthContext'
import { formatErrorMessage } from '~/lib/errors'
import { sanitizeDisplayName, sanitizeName, sanitizeSlug } from '~/lib/validate'
import { errorText } from '~/styles/shared.css'
import * as styles from './accountFields.css'

/**
 * The account's own name: the username other people address, and the display
 * name the app shows.
 *
 * ONE row rather than two, because the hub takes both in one UpdateProfile
 * and one Save writes them together. Splitting them would make each row send
 * the other field's stored value back unchanged, which is a second reader of
 * a value the row does not own.
 *
 * It needs no elevated session, and that is deliberate: a name is not a
 * credential and moves no recovery identity. The rows beneath it -- the
 * email, the password, the passkeys, the provider links -- all do, and all
 * declare `needsElevation`.
 */
export const AccountProfile: Component = () => {
  const auth = useAuth()
  const [username, setUsername] = createSignal('')
  const [displayName, setDisplayName] = createSignal('')
  const [saving, setSaving] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)

  const dirty = createMemo(() => {
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

  onMount(() => {
    const user = auth.user()
    if (user) {
      setUsername(user.username)
      setDisplayName(user.displayName)
    }
  })

  const save = async () => {
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      setMessage({ type: 'error', text: slugErr })
      return
    }
    const { value: sanitizedDisplayName, error: dnErr } = sanitizeDisplayName(displayName(), slug)
    if (dnErr) {
      setMessage({ type: 'error', text: dnErr })
      return
    }
    setSaving(true)
    setMessage(null)
    try {
      await userClient.updateProfile({ username: slug, displayName: sanitizedDisplayName })
      await auth.refreshUser()
      setDisplayName(auth.user()?.displayName ?? '')
      setUsername(auth.user()?.username ?? '')
      setMessage({ type: 'success', text: 'Profile updated.' })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to update profile') })
    }
    finally {
      setSaving(false)
    }
  }

  return (
    <div class="vstack gap-4">
      <UsernameField value={username} onInput={setUsername} labelClass={styles.fieldLabel} />
      <label class={styles.fieldLabel}>
        Display Name
        <input type="text" value={displayName()} onInput={e => setDisplayName(e.currentTarget.value)} />
      </label>
      <Show when={displayNameError()}>
        {err => <div class={errorText}>{err()}</div>}
      </Show>
      <StatusLine message={message()} />
      <div>
        <button type="button" onClick={() => void save()} disabled={saving() || !dirty() || !!displayNameError()}>
          {saving() ? 'Saving...' : 'Save Profile'}
        </button>
      </div>
    </div>
  )
}

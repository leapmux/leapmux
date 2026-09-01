import type { Component } from 'solid-js'
import { createMemo, createSignal, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { StatusLine } from '~/components/common/StatusLine'
import { UsernameField } from '~/components/common/UsernameField'
import { useAuth } from '~/context/AuthContext'
import { sanitizeDisplayName, sanitizeName, sanitizeSlug } from '~/lib/validate'
import { errorText } from '~/styles/shared.css'
import * as styles from './accountFields.css'
import { createAccountAction } from './createAccountAction'

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
  const action = createAccountAction()

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
    // eslint-disable-next-line solid/reactivity -- user-invoked action: reads at invocation time
    const [slug, slugErr] = sanitizeSlug('Username', username())
    if (slugErr) {
      action.reject(slugErr)
      return
    }
    // eslint-disable-next-line solid/reactivity -- user-invoked action: reads at invocation time
    const { value: sanitizedDisplayName, error: dnErr } = sanitizeDisplayName(displayName(), slug)
    if (dnErr) {
      action.reject(dnErr)
      return
    }
    await action.run({
      fallback: 'Failed to update profile',
      work: async () => {
        await userClient.updateProfile({ username: slug, displayName: sanitizedDisplayName })
        // The hub sanitizes both fields again, so the form adopts what it
        // stored rather than what the user typed.
        await auth.refreshUser()
        setDisplayName(auth.user()?.displayName ?? '')
        setUsername(auth.user()?.username ?? '')
        return 'Profile updated.'
      },
    })
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
      <StatusLine message={action.message()} />
      <div class={actionsFooter}>
        <button type="button" onClick={() => void save()} disabled={action.running() || !dirty() || !!displayNameError()}>
          {action.running() ? 'Saving...' : 'Save Profile'}
        </button>
      </div>
    </div>
  )
}

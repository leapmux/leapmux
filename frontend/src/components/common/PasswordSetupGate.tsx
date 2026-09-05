import type { Component } from 'solid-js'
import { createSignal } from 'solid-js'
import { authClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { USERNAME_SOLO } from '~/generated/contracts/validate'
import { formatErrorMessage } from '~/lib/errors'
import { loadSystemInfo } from '~/lib/systemInfo'
import { centeredFull, pageCard } from '~/styles/shared.css'
import { fieldLabel } from '../settings/account/accountFields.css'

/**
 * The screen that replaces the app on a passwordless solo TCP connection.
 *
 * The only useful action in that state is to set the first password.
 * `AuthGuard` renders this screen instead of the app because no other protected
 * RPC accepts the caller.
 *
 * It is reachable from the restricted PASSWORD_SETUP state. The public setup
 * RPC stores the password and an elevated session in one transaction. No
 * other protected RPC accepts the caller before that transaction commits.
 */
export const PasswordSetupGate: Component = () => {
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  /*
   * Its own pair, where the Account rows and the Network access panel share
   * `createAccountAction`. That helper carries ONE outcome channel: `work`
   * resolves to a success sentence, or it throws and reports the fallback. This
   * screen has two failures that need opposite sentences. A refused write is a
   * retry, and a re-read that fails AFTER the write must not tell the operator
   * to retry a password the account already holds -- from a screen that offers
   * no other way out. Throwing is the helper's only route to the second one, and
   * throwing is what says "the write failed".
   */
  const auth = useAuth()
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const pwProps = { password, confirmPassword }

  const submit = async () => {
    if (!passwordCanSubmit(pwProps) || busy())
      return
    setBusy(true)
    setMessage(null)
    try {
      const response = await authClient.setInitialSoloPassword({ password: password() })
      if (!response.user)
        throw new Error('The hub returned no solo user')
      // The elevation travels WITH the reply. The transaction that stored the
      // password created this session and elevated it, so the deadline is
      // known here and needs no second round trip. `setAuth` clears the
      // elevation on its own, which is right for every other caller and wrong
      // for this one.
      auth.setAuth(response.user, response.elevationExpiresAt)
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to set the password') })
      setBusy(false)
      return
    }
    // The ACCOUNT is the other copy of this fact, and it does not follow the
    // snapshot. `auth.user().passwordSet` is what Preferences -> Account renders
    // its button and its solo warning from, and nothing re-reads it for the life
    // of the page: `readCurrentUser` runs once, at bootstrap. Without this the
    // operator who just set the first password finds a screen that offers to set
    // one. It runs BEFORE the snapshot below, because that read is what takes
    // this screen down. `refreshUser` swallows its own failure, so it can never
    // report a stored password as a failed write. The two other surfaces that
    // offer this same password already refresh both copies.
    await auth.refreshUser()
    // OUTSIDE the catch above, because the password is stored by now. Forced,
    // because this snapshot is exactly what the gate reads: without it the
    // screen stays up over a hub that no longer needs it. But `loadSystemInfo`
    // rejects by design, and a refresh over a connection whose authentication
    // rule just changed is exactly where a transient failure lands -- reported
    // as "Failed to set the password" it told the operator to retry a write
    // that succeeded, from a screen that offers no other way out.
    try {
      await loadSystemInfo(true)
    }
    catch {
      setMessage({
        type: 'error',
        text: 'The password is set, but this page could not re-read the hub. Reload to continue.',
      })
    }
    finally {
      setBusy(false)
    }
  }

  return (
    <div class={centeredFull}>
      <div class={pageCard} data-testid="password-setup-gate">
        <h1>Set a password to continue</h1>
        {/*
          * The exposed address is NOT named here. The hub can answer on
          * several, and the one this page was opened at is often not the one
          * that exposes it -- a browser on 127.0.0.1 reaching a hub bound to
          * 0.0.0.0 would be told about the wrong address. Preferences →
          * Network access lists them exactly.
          */}
        <p>
          This connection uses TCP, and the “solo” account has no password.
          Set one now. Until then, TCP callers can only complete this setup.
          Every TCP address asks for the password after setup.
        </p>
        <div class="vstack gap-4">
          <label class={fieldLabel}>
            Username
            {/*
              * Read-only and pre-filled: a solo hub has exactly one account
              * and it is named "solo", so a free field would only invite a
              * name that cannot sign in.
              */}
            <input type="text" value={USERNAME_SOLO} readOnly autocomplete="username" />
          </label>
          <PasswordFields
            password={password}
            setPassword={setPassword}
            confirmPassword={confirmPassword}
            setConfirmPassword={setConfirmPassword}
            labelClass={fieldLabel}
          />
          <StatusLine message={message()} />
          <div class={actionsFooter}>
            <button
              type="button"
              onClick={() => void submit()}
              disabled={busy() || !passwordCanSubmit(pwProps)}
            >
              {busy() ? 'Setting...' : 'Set Password'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

import type { Component } from 'solid-js'
import { createSignal } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { StatusLine } from '~/components/common/StatusLine'
import { USERNAME_SOLO } from '~/generated/contracts/validate'
import { formatErrorMessage } from '~/lib/errors'
import { loadSystemInfo } from '~/lib/systemInfo'
import { centeredFull, pageCard } from '~/styles/shared.css'
import { fieldLabel } from '../settings/account/accountFields.css'

/**
 * The screen that replaces the whole app while the hub is exposed and its one
 * account has no password.
 *
 * In that state everything the app offers is offered to whoever can reach the
 * port, and no sign-in stands between them, so the one useful thing left is to
 * ask for a password. `AuthGuard` renders it instead of the app rather than
 * beside it, because a dismissible notice would leave the exposure standing.
 *
 * It is reachable only from a CREDENTIAL-FREE connection -- the hub reports
 * `password_setup_required` and `auto_authenticated` together, and a visitor
 * from a network address on such a hub is not signed in and never gets this
 * far. So the caller here is already the administrator, and asking it to prove
 * anything first would ask for the secret it is about to choose.
 *
 * ChangePassword hands this browser a session in its reply, so the app loads
 * straight after without a sign-in. That is not a convenience: storing the
 * password is what starts demanding one, and the page that made the write
 * would otherwise be signed out of the form it is standing in.
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
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

  const pwProps = { password, confirmPassword }

  const submit = async () => {
    if (!passwordCanSubmit(pwProps) || busy())
      return
    setBusy(true)
    setMessage(null)
    try {
      await userClient.changePassword({ newPassword: password() })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to set the password') })
      setBusy(false)
      return
    }
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
          This hub answers on an address other machines can reach, and it asks
          nobody for a password. Set one now. Every network address then asks
          for it, 127.0.0.1 included.
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

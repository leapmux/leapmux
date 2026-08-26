import type { Component } from 'solid-js'
import type { LinkedOAuthProvider } from '~/generated/leapmux/v1/auth_pb'
import { createSignal, For, Show } from 'solid-js'
import { useAuth } from '~/context/AuthContext'
import { elevateWithPasskey, elevateWithPassword, oauthReauthUrl } from '~/lib/elevation'
import { formatErrorMessage } from '~/lib/errors'
import { passkeyBlocker, passkeysUsableHere } from '~/lib/systemInfo'
import { passkeyBlockerMessage, passkeyErrorMessage } from '~/lib/webauthn'
import { errorText, srOnly } from '~/styles/shared.css'

/**
 * The step-up ceremony, without a container.
 *
 * It is a form BODY so the same markup serves both places a step-up is
 * needed: the standalone `/elevate` route the hub bounces a CLI login
 * through, and the in-app dialog a settings action opens. Two copies would
 * drift, and the factor list is the part that must not: an account that can
 * only elevate through its identity provider must be offered that arm
 * wherever it is asked.
 */
export const ElevateForm: Component<{
  /**
   * Called after a factor is proven and the new deadline is adopted.
   *
   * It carries no value. The form writes the deadline into the auth context
   * itself, because BOTH of its mounts did — two copies of an adoption step
   * that a third mount would have had to remember, and forgetting it leaves
   * the mirrored deadline silently stale.
   */
  onElevated: () => void
  /**
   * Where the OAuth re-authentication leg returns the browser. It leaves
   * this document, so the caller must specify an address that can resume what
   * the user was doing.
   */
  oauthRedirect: string
}> = (props) => {
  const auth = useAuth()
  const [password, setPassword] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal('')

  const passwordSet = () => auth.user()?.passwordSet === true
  const passkeyCount = () => auth.user()?.passkeyCount ?? 0
  const passkeyAvailable = () => passkeyCount() > 0 && passkeysUsableHere()
  /**
   * The reason an account whose ONLY factor is a passkey cannot verify here.
   *
   * Null while the account has no passkey, so the copy below still falls back
   * to the "nothing to verify with yet" arm for an account that holds no
   * factor at all. Non-null exactly when this form offers no passkey arm for
   * a passkey the account does hold, because `passkeyAvailable` is the same
   * two facts.
   */
  const passkeyOnlyBlocker = () => (passkeyCount() > 0 ? passkeyBlocker() : null)
  const providers = (): LinkedOAuthProvider[] => auth.user()?.oauthProviders ?? []
  /**
   * The providers the HUB will accept a step-up from.
   *
   * TWO facts, and both come from the hub. `mayElevateThroughAProvider` is the
   * ACCOUNT rule, from the same predicate the OAuth re-authentication leg
   * enforces: a provider may prove a step-up exactly when the account holds no
   * password and no passkey. `enabled` is the per-link fact: an administrator
   * can turn a provider off, and both OAuth legs then answer 403 "provider
   * disabled".
   *
   * This form used to spell the account rule out here — a second source of
   * truth for an authorization decision, and the two drifted at the first
   * change to either side.
   *
   * The form offers exactly what the hub accepts, and the reason is a dead
   * end rather than a wasted click: an arm the hub refuses is a
   * full-document navigation out of the app to a bare 403 page with no way
   * back, and it hides the copy below that gives a remedy.
   */
  const accountMayUseAProvider = () => auth.user()?.mayElevateThroughAProvider === true
  const elevatingProviders = (): LinkedOAuthProvider[] =>
    accountMayUseAProvider() ? providers().filter(p => p.enabled) : []
  const oauthOnly = () => elevatingProviders().length > 0
  const canElevate = () => passwordSet() || passkeyAvailable() || oauthOnly()

  const submitPassword = (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setMessage('')
    void (async () => {
      try {
        auth.setElevationExpiresAt(await elevateWithPassword(password()))
        props.onElevated()
      }
      catch (err) {
        // The hub answers a wrong password and a rate-limit refusal with
        // messages the user can act on, so both are shown verbatim.
        setMessage(formatErrorMessage(err, 'Could not verify your password'))
      }
      finally {
        setBusy(false)
      }
    })()
  }

  const submitPasskey = () => {
    setBusy(true)
    setMessage('')
    void (async () => {
      try {
        auth.setElevationExpiresAt(await elevateWithPasskey())
        props.onElevated()
      }
      catch (e) {
        // A dismissed platform prompt is not a failure to report.
        const text = passkeyErrorMessage(e, 'Passkey verification failed')
        if (text)
          setMessage(text)
      }
      finally {
        setBusy(false)
      }
    })()
  }

  return (
    <div class="vstack gap-4">
      <Show when={passwordSet()}>
        <form class="vstack gap-4" onSubmit={submitPassword}>
          {/*
            The account this password belongs to, for the password manager.

            A re-authentication form asks for a password and nothing else, so a
            manager has no field to match a stored entry's user against and
            fills either the wrong one or none. The remedy every manager and
            every sign-in-form guide names is the same: carry the username in
            the form as an `autocomplete="username"` field.

            It is OFFSCREEN rather than `display: none`, because a manager that
            walks the rendered fields skips a field with no box (see
            `srOnly`). It is out of the tab order and out of the
            accessibility tree: nobody types into it, and the submit reads the
            password signal rather than the DOM, so whatever a manager writes
            here reaches nothing.

            It is NOT `readonly`. A manager that fills a login form writes the
            username as well as the password, and several refuse a form whose
            username field they cannot write -- which would lose the fill this
            field exists to enable.

            This does NOT fix a password manager whose inline menu cannot draw
            over a modal <dialog>. Bitwarden's does not -- its content script
            calls hidePopover() on an element the browser reports as "in the no
            popover state" and the exception aborts the rest of its mutation
            pass (bitwarden/clients#21388, open). That is an extension bug with
            no page-side remedy short of abandoning the native modal dialog for
            every dialog in the app; the manager's toolbar and keyboard-shortcut
            fill paths do not use that menu, and this field is what makes those
            land on the right credential.
          */}
          <input
            class={srOnly}
            type="text"
            name="username"
            autocomplete="username"
            value={auth.user()?.username ?? ''}
            tabindex={-1}
            aria-hidden="true"
            data-testid="elevate-username"
          />
          <label>
            Password
            <input
              type="password"
              name="password"
              value={password()}
              onInput={e => setPassword(e.currentTarget.value)}
              autocomplete="current-password"
              data-testid="elevate-password"
            />
          </label>
          <button type="submit" disabled={busy() || !password()} data-testid="elevate-password-submit">
            {busy() ? 'Verifying…' : 'Verify'}
          </button>
        </form>
      </Show>

      <Show when={passkeyAvailable()}>
        <button type="button" onClick={submitPasskey} disabled={busy()} data-testid="elevate-passkey">
          Verify with passkey
        </button>
      </Show>

      <Show when={oauthOnly()}>
        <p>Verify with the provider you sign in with:</p>
        {/*
          elevatingProviders(), never providers(): a link the hub refuses
          leaves the app for a 403 page it could have predicted.
        */}
        <For each={elevatingProviders()}>
          {provider => (
            <a
              href={oauthReauthUrl(provider.id, props.oauthRedirect)}
              data-testid={`elevate-oauth-${provider.id}`}
            >
              Verify with
              {' '}
              {provider.name}
            </a>
          )}
        </For>
      </Show>

      {/*
        Two shapes reach this, and their remedies differ, so the copy does
        too. An account with NOTHING can still set a first password -- the
        hub admits that on a recent sign-in rather than on an elevation -- so
        the instruction is to sign in again first. An account whose only
        factor is a passkey this page cannot run gets the blocker's own
        sentence, because the three blockers have three different remedies and
        they go to three different people. The copy this replaced named the
        administrator for all of them, which is wrong advice for the two the
        BROWSER raises: no address an administrator publishes makes a
        plain-HTTP page secure.
      */}
      <Show when={!canElevate()}>
        <Show
          when={passkeyOnlyBlocker()}
          fallback={(
            <p data-testid="elevate-impossible">
              This account has nothing to verify with yet.
              Sign in again, then set a password in Preferences.
            </p>
          )}
        >
          {blocker => (
            <p data-testid="elevate-impossible">
              Your passkey is the only thing this account can verify with, and
              this page cannot run one.
              {' '}
              {passkeyBlockerMessage(blocker())}
            </p>
          )}
        </Show>
      </Show>

      <Show when={message()}>
        <div class={errorText} role="alert">{message()}</div>
      </Show>
    </div>
  )
}

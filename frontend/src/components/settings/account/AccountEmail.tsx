import type { Component } from 'solid-js'
import { A } from '@solidjs/router'
import { createMemo, createSignal, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { StatusLine } from '~/components/common/StatusLine'
import { VerificationResendControl } from '~/components/common/VerificationResendControl'
import { useAuth } from '~/context/AuthContext'
import { KEY_EMAIL_CHANGE_DRAFT, sessionStorageGet, sessionStorageRemove, sessionStorageSet } from '~/lib/browserStorage'
import { elevationPrompting } from '~/lib/elevationPrompt'
import { isEmailEnabled } from '~/lib/systemInfo'
import { useVerificationResend } from '~/lib/useVerificationResend'
import { validateEmail } from '~/lib/validate'
import { errorText, warningText } from '~/styles/shared.css'
import * as styles from './accountFields.css'
import { createAccountAction } from './createAccountAction'

/**
 * The account's email: its current state, the route to a confirmed one, and
 * the change itself.
 *
 * The address is a RECOVERY IDENTITY -- it receives the recovery link --
 * so the hub refuses to move it without a recently proven factor. Nothing
 * here checks that: the transport opens the step-up prompt on the hub's
 * refusal and retries the one refused request.
 */
export const AccountEmail: Component = () => {
  const auth = useAuth()
  // The same hook /verify-email uses, seeded from the same signal, so the
  // cooldown a send hands out survives a move between the two surfaces.
  const verification = useVerificationResend({ nextResendAvailableAt: auth.verificationResendAvailableAt })

  // The field SURVIVES a full-document round trip, because one account shape
  // is sent on one every time it changes its address.
  //
  // An account with no password and no passkey elevates only at its identity
  // provider, and that option leaves the app. Without this the user typed the
  // new address, was asked to verify, came back to an empty field and typed it
  // again -- on the shape that has no other way to verify. Every other shape
  // proves its factor in the dialog and never leaves, so for them this only
  // means a reload no longer loses the field either.
  //
  // Through the storage gateway, so the value is scoped to the account and
  // carries an expiry; see KEY_EMAIL_CHANGE_DRAFT for why the store is the
  // session and not the browser.
  const [newEmail, setNewEmail] = createSignal(
    sessionStorageGet<string>(KEY_EMAIL_CHANGE_DRAFT) ?? '',
  )

  /** Write the field through, so the address is there when the browser returns. */
  const editEmail = (next: string) => {
    setNewEmail(next)
    if (next === '')
      sessionStorageRemove(KEY_EMAIL_CHANGE_DRAFT)
    else
      sessionStorageSet(KEY_EMAIL_CHANGE_DRAFT, next)
  }

  const action = createAccountAction()

  const sameAsCurrent = createMemo(() => {
    const trimmed = newEmail().trim().toLowerCase()
    return trimmed !== '' && trimmed === (auth.user()?.email ?? '').toLowerCase()
  })

  const requestChange = async () => {
    const email = newEmail().trim()
    if (!email) {
      action.reject('Email must not be empty.')
      return
    }
    const emailErr = validateEmail(email)
    if (emailErr) {
      action.reject(emailErr)
      return
    }
    // No step-up wrapper here. The transport opens the prompt on the hub's
    // refusal and retries this request ONCE, so what the failure path reports
    // is a second refusal or a failure a prompt cannot fix.
    await action.run({
      fallback: 'Failed to request email change',
      work: async () => {
        const resp = await userClient.requestEmailChange({ newEmail: email })
        // The ordinary end of the draft. The TTL is only the backstop for a
        // round trip the user never finished.
        editEmail('')
        // AWAITED, and inside the work: `busy` clears when this resolves, so
        // an unawaited refresh would re-enable the button while the user still
        // carries the old address and no pending change.
        await auth.refreshUser()
        return resp.verificationRequired
          ? 'Verification email sent. Check your inbox.'
          : 'Email updated.'
      },
    })
  }

  return (
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
      {/*
        The control this surface owes an unverified address, and the one the
        docs already point operators at.

        Nothing else offers it. `verificationStatusFor` short-circuits on
        IsAdmin, so the app never routes an administrator to /verify-email, and
        "Change Email" writes an administrator's new address straight to the
        column with no code at all. So the /setup administrator -- who lands
        UNVERIFIED, because the column records confirmation and not privilege
        -- had no route to a confirmed address, and account recovery, the
        worker-instructions mail and the CLI-credential notice all stayed
        silently off for them.

        Resend is the leg that works: it seeds a pending row from the stored
        address when there is none.
      */}
      <Show when={auth.user()?.email && !auth.user()?.emailVerified && isEmailEnabled()}>
        <VerificationResendControl
          resend={verification}
          footerExtra={<A href="/verify-email">Enter the code</A>}
        />
      </Show>
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
          onInput={e => editEmail(e.currentTarget.value)}
          placeholder="Enter new email address"
        />
      </label>
      <StatusLine message={action.message()} />
      <Show when={sameAsCurrent()}>
        <div class={errorText}>This is already your current email.</div>
      </Show>
      <div class={actionsFooter}>
        <button
          type="button"
          onClick={() => void requestChange()}
          disabled={action.running() || elevationPrompting() || !newEmail().trim() || sameAsCurrent()}
        >
          {action.running() ? 'Requesting...' : 'Change Email'}
        </button>
      </div>
    </div>
  )
}

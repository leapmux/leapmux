import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import { A } from '@solidjs/router'
import { createMemo, createSignal, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import { formatErrorMessage } from '~/lib/errors'
import { isEmailEnabled } from '~/lib/systemInfo'
import { useVerificationResend } from '~/lib/useVerificationResend'
import { validateEmail } from '~/lib/validate'
import { errorText, successText, warningText } from '~/styles/shared.css'
import * as styles from './accountFields.css'

/**
 * The account's email: its current state, the route to a confirmed one, and
 * the change itself.
 *
 * The address is a RECOVERY IDENTITY -- it receives the password-reset link --
 * so the hub refuses to move it without a recently proven factor. Nothing
 * here checks that: the transport opens the step-up prompt on the hub's
 * refusal and retries the one refused request.
 */
export const AccountEmail: Component = () => {
  const auth = useAuth()
  // The same hook /verify-email uses, seeded from the same signal, so the
  // cooldown a send hands out survives a move between the two surfaces.
  const verification = useVerificationResend({ nextResendAvailableAt: auth.verificationResendAvailableAt })

  const [newEmail, setNewEmail] = createSignal('')
  const [saving, setSaving] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)

  const sameAsCurrent = createMemo(() => {
    const trimmed = newEmail().trim().toLowerCase()
    return trimmed !== '' && trimmed === (auth.user()?.email ?? '').toLowerCase()
  })

  const requestChange = async () => {
    const email = newEmail().trim()
    if (!email) {
      setMessage({ type: 'error', text: 'Email must not be empty.' })
      return
    }
    const emailErr = validateEmail(email)
    if (emailErr) {
      setMessage({ type: 'error', text: emailErr })
      return
    }
    setSaving(true)
    setMessage(null)
    try {
      const resp = await userClient.requestEmailChange({ newEmail: email })
      if (resp.verificationRequired) {
        setMessage({ type: 'success', text: 'Verification email sent. Check your inbox.' })
      }
      else {
        setMessage({ type: 'success', text: 'Email updated.' })
      }
      setNewEmail('')
      auth.refreshUser()
    }
    catch (e) {
      // No step-up wrapper here. The transport opens the prompt on the hub's
      // refusal and retries this request ONCE, so what reaches this catch is a
      // second refusal or a failure a prompt cannot fix.
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to request email change') })
    }
    finally {
      setSaving(false)
    }
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
        -- had no route to a confirmed address, and Forgot password, the
        worker-instructions mail and the CLI-credential notice all stayed
        silently off for them.

        Resend is the leg that works: it seeds a pending row from the stored
        address when there is none.
      */}
      <Show when={auth.user()?.email && !auth.user()?.emailVerified && isEmailEnabled()}>
        <div class="hstack gap-2">
          <button type="button" onClick={() => void verification.resend()} disabled={verification.disabled()}>
            {verification.buttonLabel()}
          </button>
          <A href="/verify-email">Enter the code</A>
        </div>
        <Show when={verification.status()}>
          {msg => <div class={successText}>{msg()}</div>}
        </Show>
        <Show when={verification.error()}>
          {msg => <div class={errorText}>{msg()}</div>}
        </Show>
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
          onInput={e => setNewEmail(e.currentTarget.value)}
          placeholder="Enter new email address"
        />
      </label>
      <StatusLine message={message()} />
      <Show when={sameAsCurrent()}>
        <div class={errorText}>This is already your current email.</div>
      </Show>
      <div>
        <button
          type="button"
          onClick={() => void requestChange()}
          disabled={saving() || elevationPrompting() || !newEmail().trim() || sameAsCurrent()}
        >
          {saving() ? 'Requesting...' : 'Change Email'}
        </button>
      </div>
    </div>
  )
}

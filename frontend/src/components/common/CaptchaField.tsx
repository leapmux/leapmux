import type { Component } from 'solid-js'
import { Match, Show, Switch } from 'solid-js'

import { CaptchaProvider } from '~/generated/leapmux/v1/auth_pb'
import { getCaptchaProvider } from '~/lib/systemInfo'
import { AltchaField } from './AltchaField'
import { RecaptchaV3Field } from './RecaptchaV3Field'
import { TurnstileField } from './TurnstileField'

/** Imperative handle for the parent form, handed back through `ref`. */
export interface CaptchaFieldHandle {
  /** Discard any solved payload and re-arm for a fresh one. */
  reset: () => void
}

// CaptchaFieldProps is exported for CaptchaSection, which types its action
// prop with the same union the hub's protectedProcedures map carries.
export interface CaptchaFieldProps {
  /**
   * The action the token is minted under: 'login', 'signup', or
   * 'complete_signup'. External providers bind it into the token and the
   * hub refuses mismatches; ALTCHA ignores it.
   */
  action: 'login' | 'signup' | 'complete_signup' | 'passkey_login' | 'passkey_signup' | 'password_reset' | 'complete_password_reset'
  /** Receives the provider token once solved, null otherwise. */
  onPayload: (payload: string | null) => void
  /**
   * ALTCHA-only: the hub answered with no challenge (captcha disabled
   * since the system-info snapshot loaded), so the form lifts its
   * requirement instead of dead-locking. External providers never stand
   * down — a script that fails to load stays an error, not an open door.
   */
  onUnavailable: () => void
  ref?: (handle: CaptchaFieldHandle) => void
}

// Dispatches to the selected provider's field. The provider comes from
// system info as a signal, so a runtime provider switch (picked up by the
// denial-driven reload) re-evaluates the Switch and mounts the new field
// without a page refresh. Anything but the two external enum values falls
// back to ALTCHA, which the hub serves whenever it cannot resolve the
// selected row — the safe default on both ends.
//
// Only the external providers key on the action: they bind the action into
// the token, so a Password/Passkey toggle must mint a fresh one or the hub
// denies it. The keyed Show is the whole mechanism -- it disposes the field,
// and createCaptchaFieldBase's onCleanup then tears the widget down and
// clears the payload together, which an in-field effect would have to
// re-derive by hand. Neither approach avoids the re-solve, because the
// action is bound at render/execute time either way.
//
// ALTCHA ignores the action entirely (the hub drops it), so it stays
// mounted across Password/Passkey toggles and keeps the user's solved
// challenge.
export const CaptchaField: Component<CaptchaFieldProps> = (props) => {
  return (
    <Switch fallback={<AltchaField onPayload={props.onPayload} onUnavailable={props.onUnavailable} ref={props.ref} />}>
      <Match when={getCaptchaProvider() === CaptchaProvider.TURNSTILE}>
        <Show when={props.action} keyed>
          {action => <TurnstileField action={action} onPayload={props.onPayload} ref={props.ref} />}
        </Show>
      </Match>
      <Match when={getCaptchaProvider() === CaptchaProvider.RECAPTCHA_V3}>
        <Show when={props.action} keyed>
          {action => <RecaptchaV3Field action={action} onPayload={props.onPayload} ref={props.ref} />}
        </Show>
      </Match>
    </Switch>
  )
}

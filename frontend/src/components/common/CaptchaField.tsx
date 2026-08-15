import type { Component } from 'solid-js'
import { Match, Switch } from 'solid-js'

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

interface CaptchaFieldProps {
  /**
   * The action the token is minted under: 'login', 'signup', or
   * 'complete_signup'. External providers bind it into the token and the
   * hub refuses mismatches; ALTCHA ignores it.
   */
  action: 'login' | 'signup' | 'complete_signup'
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
export const CaptchaField: Component<CaptchaFieldProps> = (props) => {
  return (
    <Switch fallback={<AltchaField onPayload={props.onPayload} onUnavailable={props.onUnavailable} ref={props.ref} />}>
      <Match when={getCaptchaProvider() === CaptchaProvider.TURNSTILE}>
        <TurnstileField action={props.action} onPayload={props.onPayload} ref={props.ref} />
      </Match>
      <Match when={getCaptchaProvider() === CaptchaProvider.RECAPTCHA_V3}>
        <RecaptchaV3Field action={props.action} onPayload={props.onPayload} ref={props.ref} />
      </Match>
    </Switch>
  )
}

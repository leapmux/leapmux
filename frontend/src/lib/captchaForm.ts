import type { CaptchaFieldHandle } from '~/components/common/CaptchaField'
import { Code, ConnectError } from '@connectrpc/connect'
import { createSignal } from 'solid-js'
import { isCaptchaEnabled, isCaptchaUnsolvableHere, isSystemInfoLoaded, refreshSnapshot } from './systemInfo'

export interface CaptchaRequestFields {
  captchaPayload: string
  honeypot: string
}

export interface CaptchaFormState {
  /**
   * True until the system info answers. The forms fail closed meanwhile:
   * submitting against an unknown captcha policy sends an empty payload
   * the hub denies, so the button waits for the answer instead.
   */
  pending: () => boolean
  /** The hub requires a solved captcha. Reactive; false once stood down. */
  required: () => boolean
  /** The solved payload; null until the widget verifies. */
  payload: () => string | null
  setPayload: (payload: string | null) => void
  /** The honeypot value; the form sends it on every protected request. */
  honeypot: () => string
  setHoneypot: (value: string) => void
  /** Wire the CaptchaField's imperative ref here. */
  bindField: (handle: CaptchaFieldHandle) => void
  /**
   * The hub answered "no challenge" (captcha disabled since the snapshot
   * loaded): the requirement lifts until a reset re-arms.
   */
  noteUnavailable: () => void
  /**
   * Discard the solve, clear the honeypot, and re-arm the field. Pass the
   * rejected submit's error: a captcha denial (the hub's uniform
   * PermissionDenied) also refreshes the system-info snapshot, because the
   * denial is the one signal that the captcha policy may have changed
   * since the page loaded.
   */
  reset: (err?: unknown) => void
  /**
   * True when the hub requires ALTCHA and this page cannot solve it. The
   * form shows the explanation and blocks; see isCaptchaUnsolvableHere.
   */
  unsolvable: () => boolean
  /** Submit-button gate: blocks while a payload is required and missing. */
  blocksSubmit: () => boolean
  /** Request fields for Login/SignUp/CompleteOAuthSignup. */
  fields: () => CaptchaRequestFields
}

/**
 * createCaptchaForm owns the captcha lifecycle one protected form needs:
 * the payload and honeypot state, the reactive requirement gate, the
 * fail-closed bootstrap window, and the reset path. The three credential
 * forms consume this instead of re-wiring the same six pieces — they
 * already drifted on the bootstrap gate, which hid the widget on direct
 * page loads.
 */
export function createCaptchaForm(): CaptchaFormState {
  const [payload, setPayload] = createSignal<string | null>(null)
  const [honeypot, setHoneypot] = createSignal('')
  const [stoodDown, setStoodDown] = createSignal(false)
  let field: CaptchaFieldHandle | undefined

  const pending = () => !isSystemInfoLoaded()
  const unsolvable = () => isSystemInfoLoaded() && isCaptchaUnsolvableHere()
  // `required` means "mount a widget and collect a payload", so an
  // unsolvable page is NOT required: no widget can mount there. It blocks
  // submission instead (see blocksSubmit), so the form never sends a request
  // the hub is certain to deny. This is the ONE place that combines the hub's
  // answer and the page's ability; isCaptchaEnabled reports the hub alone.
  const required = () => !stoodDown() && isSystemInfoLoaded() && isCaptchaEnabled() && !unsolvable()

  return {
    pending,
    required,
    unsolvable,
    payload,
    setPayload,
    honeypot,
    setHoneypot,
    bindField: (handle) => {
      field = handle
    },
    noteUnavailable: () => {
      setStoodDown(true)
    },
    reset: (err?: unknown) => {
      // A rejected attempt must not linger: the consumed payload and a
      // honeypot value an autofill heuristic dropped into the hidden
      // input both make the retry fail identically.
      setStoodDown(false)
      setPayload(null)
      setHoneypot('')
      try {
        field?.reset()
      }
      catch {
        // A keyed remount (Password/Passkey) can leave the previous
        // handle pointing at a widget that is already gone. Payload
        // and honeypot are already cleared; the new field arms itself.
      }
      // A captcha denial is also the signal that the captcha snapshot in
      // systemInfo is stale: the admin may have enabled or disabled
      // captcha, or switched providers, since the page loaded. The
      // refreshed signals re-mount the right provider's field (or stand
      // down) for the retry, so every protected form converges after one
      // denial instead of failing identically forever. Other failures --
      // a wrong password, a network fault -- cannot change the captcha
      // policy, so they skip the extra fetch. The deduped refresh shares
      // one round trip with the field-level arm-failure refreshes the
      // same denial triggers.
      if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
        refreshSnapshot()
      }
    },
    blocksSubmit: () => pending() || unsolvable() || (required() && payload() === null),
    fields: () => ({ captchaPayload: payload() ?? '', honeypot: honeypot() }),
  }
}

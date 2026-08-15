import type { CaptchaFieldHandle } from '~/components/common/CaptchaField'
import { createSignal } from 'solid-js'
import { isCaptchaEnabled, isSystemInfoLoaded } from './systemInfo'

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
  /** The honeypot value; it rides on every protected request. */
  honeypot: () => string
  setHoneypot: (value: string) => void
  /** Wire the CaptchaField's imperative ref here. */
  bindField: (handle: CaptchaFieldHandle) => void
  /**
   * The hub answered "no challenge" (captcha disabled since the snapshot
   * loaded): the requirement lifts until a reset re-arms.
   */
  noteUnavailable: () => void
  /** Discard the solve, clear the honeypot, and re-arm the field. */
  reset: () => void
  /** Submit-button gate: blocks while a payload is required and missing. */
  blocksSubmit: () => boolean
  /** Request fields for Login/SignUp/CompleteOAuthSignup. */
  fields: () => CaptchaRequestFields
}

/**
 * createCaptchaForm owns the captcha lifecycle one protected form needs:
 * the payload and honeypot state, the reactive requirement gate, the
 * fail-closed bootstrap window, and the reset path. The three credential
 * forms consume this instead of re-wiring the same six pieces — they had
 * already drifted on the bootstrap gate, which hid the widget on direct
 * page loads.
 */
export function createCaptchaForm(): CaptchaFormState {
  const [payload, setPayload] = createSignal<string | null>(null)
  const [honeypot, setHoneypot] = createSignal('')
  const [stoodDown, setStoodDown] = createSignal(false)
  let field: CaptchaFieldHandle | undefined

  const pending = () => !isSystemInfoLoaded()
  const required = () => !stoodDown() && isSystemInfoLoaded() && isCaptchaEnabled()

  return {
    pending,
    required,
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
    reset: () => {
      // A rejected attempt must not linger: the consumed payload and a
      // honeypot value an autofill heuristic dropped into the hidden
      // input both poison the retry identically.
      setStoodDown(false)
      setPayload(null)
      setHoneypot('')
      field?.reset()
    },
    blocksSubmit: () => pending() || (required() && payload() === null),
    fields: () => ({ captchaPayload: payload() ?? '', honeypot: honeypot() }),
  }
}

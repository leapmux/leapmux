import { createEffect, createSignal } from 'solid-js'

// The fake widget holds its payload in a signal the test controls, so a
// test can keep the form unsolved (submit disabled) and release it. The
// mock also captures the unavailable callback so a test can simulate the
// hub answering "no challenge" (captcha disabled at runtime). Per-file
// state: Vitest gives every test file its own module registry, the same
// isolation systemInfoMock relies on.
const [mockCaptchaPayload, setMockCaptchaPayload] = createSignal<string | null>(null)
let captchaUnavailable: (() => void) | undefined

export const captchaFieldMock = {
  CaptchaField: (props: { action: string, onPayload: (p: string | null) => void, onUnavailable: () => void }) => {
    // Captured for the stand-down test; the primitive's callback is a stable
    // closure, so an untracked read is fine.
    /* eslint-disable solid/reactivity -- stable callback captured for tests */
    captchaUnavailable = props.onUnavailable
    /* eslint-enable solid/reactivity */
    createEffect(() => props.onPayload(mockCaptchaPayload()))
    return <div data-testid="captcha-field" data-action={props.action} />
  },
}

export const captchaHoneypotMock = {
  CaptchaHoneypot: (props: { value: string, onInput: (v: string) => void }) => (
    <input
      data-testid="website-field"
      type="text"
      name="website"
      value={props.value}
      onInput={e => props.onInput(e.currentTarget.value)}
    />
  ),
}

export { setMockCaptchaPayload }

export function fireCaptchaUnavailable(): void {
  captchaUnavailable?.()
}

/** Roll the widget back to unsolved and drop the captured callback. */
export function resetCaptchaMocks(): void {
  setMockCaptchaPayload(null)
  captchaUnavailable = undefined
}

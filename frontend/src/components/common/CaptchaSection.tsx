import type { Component } from 'solid-js'
import type { CaptchaFieldProps } from './CaptchaField'
import type { CaptchaFormState } from '~/lib/captchaForm'

import { Show } from 'solid-js'
import { CaptchaField } from './CaptchaField'
import { CaptchaHoneypot } from './CaptchaHoneypot'

interface CaptchaSectionProps {
  /** The action the token is minted under; one per protected form. */
  action: CaptchaFieldProps['action']
  /** The form's captcha state, from createCaptchaForm. */
  captcha: CaptchaFormState
}

/**
 * The honeypot + captcha-field wiring every protected form renders. One
 * component keeps the honeypot/requirement-gate contract identical across
 * the login, signup, and complete-signup forms — they had already drifted
 * on this wiring once before.
 */
export const CaptchaSection: Component<CaptchaSectionProps> = (props) => {
  return (
    <>
      <CaptchaHoneypot value={props.captcha.honeypot()} onInput={props.captcha.setHoneypot} />
      <Show when={props.captcha.required()}>
        <CaptchaField
          action={props.action}
          ref={props.captcha.bindField}
          onPayload={props.captcha.setPayload}
          onUnavailable={props.captcha.noteUnavailable}
        />
      </Show>
    </>
  )
}

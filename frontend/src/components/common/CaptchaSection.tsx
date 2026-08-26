import type { Component } from 'solid-js'
import type { CaptchaFieldProps } from './CaptchaField'
import type { CaptchaFormState } from '~/lib/captchaForm'

import { Show } from 'solid-js'
import { CaptchaField } from './CaptchaField'
import * as styles from './CaptchaField.css'
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
      {/*
        The hub requires ALTCHA and this page cannot solve it. Saying so
        and blocking is the only honest option: no widget can mount here,
        and submitting anyway would fail forever with a message that specifies
        nothing the user or the operator can act on.
      */}
      <Show when={props.captcha.unsolvable()}>
        <div class={styles.loadError} role="alert">
          This page cannot run the human-verification check, because the browser
          treats it as an insecure connection. Open LeapMux at the address the
          operator published, over HTTPS, and try again. An operator who sees
          this at the address users really type must point
          {' '}
          <code>public_url</code>
          {' '}
          at that address instead.
        </div>
      </Show>
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

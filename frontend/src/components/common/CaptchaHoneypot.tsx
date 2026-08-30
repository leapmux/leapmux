import type { Component } from 'solid-js'

import * as styles from './CaptchaField.css'

/**
 * The hidden anti-bot honeypot input, rendered separately from every
 * provider's widget: the server checks it on every protected procedure
 * regardless of captcha enablement or provider, so it must not disappear
 * when the widget does or change with the provider.
 *
 * Every string this renders is readable by a bot that walks the DOM, so
 * nothing here may name the trap: the field poses as an ordinary
 * "website" input and the class is `websiteField` for the reason
 * CaptchaField.css.ts states.
 */
export const CaptchaHoneypot: Component<{ value: string, onInput: (value: string) => void }> = props => (
  <input
    class={styles.websiteField}
    type="text"
    name="website"
    tabindex={-1}
    autocomplete="off"
    aria-hidden="true"
    value={props.value}
    onInput={e => props.onInput(e.currentTarget.value)}
  />
)

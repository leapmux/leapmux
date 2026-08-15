import type { Component } from 'solid-js'

import * as styles from './CaptchaField.css'

/**
 * The hidden anti-bot honeypot input, rendered separately from every
 * provider's widget: the server checks it on every protected procedure
 * regardless of captcha enablement or provider, so it must not disappear
 * when the widget does or change with the provider.
 */
export const CaptchaHoneypot: Component<{ value: string, onInput: (value: string) => void }> = props => (
  <input
    class={styles.honeypot}
    type="text"
    name="website"
    tabindex={-1}
    autocomplete="off"
    aria-hidden="true"
    value={props.value}
    onInput={e => props.onInput(e.currentTarget.value)}
  />
)

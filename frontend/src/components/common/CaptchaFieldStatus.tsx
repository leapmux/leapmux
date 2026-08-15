import type { Component } from 'solid-js'
import { Show } from 'solid-js'

import * as styles from './CaptchaField.css'
import { Spinner } from './Spinner'

interface CaptchaFieldStatusProps {
  /** True once the provider's widget armed a usable token. */
  ready: boolean
  /** True when the provider's script or challenge failed to load. */
  loadError: boolean
}

/**
 * The loading/error affordance every captcha provider field shows while
 * its script loads or after the load failed. One component keeps the
 * copy and the contract in one place, so a fourth provider field copies
 * none of it.
 */
export const CaptchaFieldStatus: Component<CaptchaFieldStatusProps> = (props) => {
  return (
    <>
      <Show when={!props.ready && !props.loadError}>
        <div class={styles.loading}>
          <Spinner size="sm" />
          Preparing verification…
        </div>
      </Show>
      <Show when={props.loadError}>
        <div class={styles.loadError}>
          Could not load the human-verification challenge. Check your connection and reload the page.
        </div>
      </Show>
    </>
  )
}

import type { AltchaWidgetElement } from 'altcha'
import type { Component } from 'solid-js'
import { createSignal, onCleanup, onMount, Show } from 'solid-js'

import { fetchCaptchaChallenge } from '~/lib/captchaChallenge'
import * as styles from './CaptchaField.css'
import { Spinner } from './Spinner'
import 'altcha'

/** Imperative handle for the parent form, handed back through `ref`. */
export interface CaptchaFieldHandle {
  /** Discard any solved payload and re-arm with a fresh challenge. */
  reset: () => void
}

interface CaptchaFieldProps {
  /** Receives the base64 ALTCHA payload once solved, null otherwise. */
  onPayload: (payload: string | null) => void
  /**
   * The hub answered with no challenge (captcha disabled since the
   * system-info snapshot loaded). The form lifts its requirement instead
   * of dead-locking on a challenge that never arrives.
   */
  onUnavailable: () => void
  ref?: (handle: CaptchaFieldHandle) => void
}

/**
 * The honeypot input, rendered separately from the widget: the server
 * checks it on every protected procedure regardless of captcha
 * enablement, so it must not disappear when the widget does.
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

// The ALTCHA widget, themed to Oat (see CaptchaField.css.ts). The
// challenge is fetched from the hub (not challengeurl) so it rides the
// same ConnectRPC transport and auth surface as everything else, then
// handed to the widget programmatically.
export const CaptchaField: Component<CaptchaFieldProps> = (props) => {
  let widget: AltchaWidgetElement | undefined
  let disposed = false
  // Re-arms can overlap (a reset while a fetch is in flight); the epoch
  // keeps a slower, older fetch from overwriting the newer challenge.
  let armEpoch = 0
  const [ready, setReady] = createSignal(false)
  const [loadError, setLoadError] = createSignal(false)

  const arm = async () => {
    const epoch = ++armEpoch
    setLoadError(false)
    try {
      const challenge = await fetchCaptchaChallenge()
      if (disposed || epoch !== armEpoch || !widget)
        return
      if (!challenge) {
        props.onUnavailable()
        return
      }
      await widget.configure({ challenge, auto: 'off' })
      if (disposed || epoch !== armEpoch || !widget)
        return
      setReady(true)
    }
    catch {
      if (!disposed && epoch === armEpoch)
        setLoadError(true)
    }
  }

  const handleStateChange = (ev: Event) => {
    const detail = (ev as CustomEvent<{ state: string, payload?: string }>).detail
    if (detail.state === 'verified' && detail.payload) {
      props.onPayload(detail.payload)
    }
    else {
      props.onPayload(null)
      // An expired challenge (idle on the form too long) or a solve error
      // needs a fresh challenge before the user can try again.
      if (detail.state === 'expired' || detail.state === 'error') {
        widget?.reset()
        void arm()
      }
    }
  }

  onMount(() => {
    widget?.addEventListener('statechange', handleStateChange)
    void arm()
    props.ref?.({
      reset: () => {
        armEpoch++
        props.onPayload(null)
        setReady(false)
        widget?.reset()
        void arm()
      },
    })
  })

  onCleanup(() => {
    disposed = true
    armEpoch++
    widget?.removeEventListener('statechange', handleStateChange)
    props.onPayload(null)
  })

  return (
    <div class={styles.field}>
      <altcha-widget ref={widget} />
      <Show when={!ready() && !loadError()}>
        <div class={styles.loading}>
          <Spinner size="sm" />
          Preparing verification…
        </div>
      </Show>
      <Show when={loadError()}>
        <div class={styles.loadError}>
          Could not load the human-verification challenge. Check your connection and reload the page.
        </div>
      </Show>
    </div>
  )
}

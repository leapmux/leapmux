import type { AltchaWidgetElement } from 'altcha'
import type { Component } from 'solid-js'
import type { CaptchaFieldHandle } from './CaptchaField'

import { createSignal, onCleanup, onMount } from 'solid-js'
import { fetchAltchaChallenge } from '~/lib/altchaChallenge'
import * as styles from './CaptchaField.css'
import { CaptchaFieldStatus } from './CaptchaFieldStatus'
import 'altcha'

interface AltchaFieldProps {
  /** Receives the base64 ALTCHA payload once solved, null otherwise. */
  onPayload: (payload: string | null) => void
  /**
   * The hub answered with no challenge (captcha disabled since the
   * system-info snapshot loaded). The form lifts its requirement instead
   * of dead-locking on a challenge that never arrives. Any other answer
   * — another provider selected, a transport fault — is an error, not a
   * stand-down: a stale altcha widget must never open a tokenless door.
   */
  onUnavailable: () => void
  ref?: (handle: CaptchaFieldHandle) => void
}

// The ALTCHA widget, themed to Oat (see CaptchaField.css.ts). The
// challenge is fetched from the hub (not challengeurl) so it rides the
// same ConnectRPC transport and auth surface as everything else, then
// handed to the widget programmatically.
export const AltchaField: Component<AltchaFieldProps> = (props) => {
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
      const challenge = await fetchAltchaChallenge()
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
      <CaptchaFieldStatus ready={ready()} loadError={loadError()} />
    </div>
  )
}

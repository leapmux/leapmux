import type { AltchaWidgetElement } from 'altcha'
import type { Component } from 'solid-js'
import type { CaptchaFieldHandle } from './CaptchaField'

import { onMount } from 'solid-js'
import { fetchAltchaChallenge } from '~/lib/altchaChallenge'
import { createCaptchaFieldBase, noteFieldArmFailure } from './captchaFieldBase'
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
  // Re-arms can overlap (a reset while a fetch is in flight); the epoch
  // keeps a slower, older fetch from overwriting the newer challenge.
  let armEpoch = 0
  // props.onPayload is a stable callback the base only invokes from
  // user-driven callbacks and cleanup; it is not a tracked read.
  // eslint-disable-next-line solid/reactivity
  const base = createCaptchaFieldBase(props.onPayload, () => {
    armEpoch++
    widget?.removeEventListener('statechange', handleStateChange)
  })

  const arm = async () => {
    const epoch = ++armEpoch
    base.setLoadError(false)
    try {
      const challenge = await fetchAltchaChallenge()
      if (base.disposed() || epoch !== armEpoch || !widget)
        return
      if (!challenge) {
        props.onUnavailable()
        return
      }
      await widget.configure({ challenge, auto: 'off' })
      if (base.disposed() || epoch !== armEpoch || !widget)
        return
      base.setReady(true)
    }
    catch {
      if (!base.disposed() && epoch === armEpoch) {
        base.setLoadError(true)
        // The fetch can fail because the provider switched since the
        // system-info snapshot loaded (the hub answers
        // FailedPrecondition). One deduped refresh re-mounts the right
        // provider's field through the CaptchaField Switch; without it the
        // disabled submit button can never trigger the denial-driven
        // reload, and the form dead-ends until a manual page reload. A
        // transient network fault costs one extra system-info fetch that
        // changes nothing.
        noteFieldArmFailure()
      }
    }
  }

  function resetWidget() {
    // reset() is not always a function: the element can still be mounting,
    // or a keyed remount (Password/Passkey action switch) can tear it
    // down while this handle still runs. A throw here used to skip arm()
    // and leave the checkbox dead.
    try {
      if (typeof widget?.reset === 'function')
        widget.reset()
    }
    catch {
      // Ignore teardown and pre-configure failures.
    }
  }

  function handleStateChange(ev: Event) {
    const detail = (ev as CustomEvent<{ state: string, payload?: string }>).detail
    if (detail.state === 'verified' && detail.payload) {
      props.onPayload(detail.payload)
    }
    else {
      props.onPayload(null)
      // An expired challenge (idle on the form too long) or a solve error
      // needs a fresh challenge before the user can try again.
      if (detail.state === 'expired' || detail.state === 'error') {
        resetWidget()
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
        base.setReady(false)
        resetWidget()
        if (!base.disposed())
          void arm()
      },
    })
  })

  return (
    <base.Field>
      <altcha-widget ref={widget} />
    </base.Field>
  )
}

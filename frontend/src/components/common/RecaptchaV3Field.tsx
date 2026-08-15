import type { Component } from 'solid-js'
import type { CaptchaFieldHandle } from './CaptchaField'

import { createSignal, onCleanup, onMount } from 'solid-js'
import { loadExternalScript } from '~/lib/scriptLoader'
import { getCaptchaSiteKey } from '~/lib/systemInfo'
import * as styles from './CaptchaField.css'
import { CaptchaFieldStatus } from './CaptchaFieldStatus'

// The v3 script is loaded with the site key as its render parameter; it
// injects the reCAPTCHA badge itself, which stays visible (Google's terms
// connect the badge with using the service on a page).
const RECAPTCHA_SCRIPT_URL = 'https://www.google.com/recaptcha/api.js?render='

// reCAPTCHA v3 tokens expire after two minutes. Re-execute comfortably
// inside the window so a form is never armed with a token that dies
// before the user submits.
const RECAPTCHA_REFRESH_MS = 110_000

interface RecaptchaV3FieldProps {
  /**
   * The action this form's tokens are minted under; the hub refuses
   * tokens whose action does not match the protected procedure.
   */
  action: string
  /** Receives the single-use reCAPTCHA v3 token, null while none is held. */
  onPayload: (payload: string | null) => void
  ref?: (handle: CaptchaFieldHandle) => void
}

// Google reCAPTCHA v3 is invisible and score-based: there is no widget to
// click, only a token the script mints on demand. This component keeps a
// fresh token armed (re-executing inside the two-minute validity window)
// so the form's submit stays unblocked; the score decision happens
// server-side at siteverify. A hidden tab cannot submit, so the refresh
// skips it — idle and background tabs must not mint tokens nobody uses —
// and re-arms the moment the tab becomes visible again.
export const RecaptchaV3Field: Component<RecaptchaV3FieldProps> = (props) => {
  let disposed = false
  // Re-executions can overlap (a reset while one is in flight); the epoch
  // keeps a slower, older execute from overwriting the newer token.
  let epoch = 0
  let refreshTimer: ReturnType<typeof setInterval> | undefined
  const [ready, setReady] = createSignal(false)
  const [loadError, setLoadError] = createSignal(false)

  const execute = async () => {
    const current = ++epoch
    const action = props.action
    setLoadError(false)
    try {
      const siteKey = getCaptchaSiteKey()
      await loadExternalScript(`${RECAPTCHA_SCRIPT_URL}${encodeURIComponent(siteKey)}`)
      if (disposed || current !== epoch || !window.grecaptcha)
        return
      // grecaptcha.ready fires immediately once the script loads.
      const token = await new Promise<string>((resolve, reject) => {
        window.grecaptcha!.ready(() => {
          window.grecaptcha!.execute(siteKey, { action }).then(resolve, reject)
        })
      })
      if (disposed || current !== epoch)
        return
      setReady(true)
      props.onPayload(token)
    }
    catch {
      if (!disposed && current === epoch) {
        setReady(false)
        setLoadError(true)
        props.onPayload(null)
      }
    }
  }

  const onVisibilityChange = () => {
    if (!document.hidden)
      void execute()
  }

  onMount(() => {
    void execute()
    refreshTimer = setInterval(() => {
      if (!document.hidden)
        void execute()
    }, RECAPTCHA_REFRESH_MS)
    document.addEventListener('visibilitychange', onVisibilityChange)
    props.ref?.({
      reset: () => void execute(),
    })
  })

  onCleanup(() => {
    disposed = true
    epoch++
    if (refreshTimer !== undefined)
      clearInterval(refreshTimer)
    document.removeEventListener('visibilitychange', onVisibilityChange)
    props.onPayload(null)
  })

  return (
    <div class={styles.field}>
      <CaptchaFieldStatus ready={ready()} loadError={loadError()} />
    </div>
  )
}

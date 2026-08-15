import type { Component } from 'solid-js'
import type { CaptchaFieldHandle } from './CaptchaField'

import { createEffect, createSignal, onCleanup, onMount } from 'solid-js'
import { loadExternalScript } from '~/lib/scriptLoader'
import { getCaptchaSiteKey } from '~/lib/systemInfo'
import * as styles from './CaptchaField.css'
import { CaptchaFieldStatus } from './CaptchaFieldStatus'

// The widget script must come from Cloudflare's exact URL — proxying or
// caching it breaks future updates (per Turnstile's docs). Explicit
// rendering keeps the mount under our control instead of the script's
// implicit .cf-turnstile scan.
const TURNSTILE_SCRIPT_URL = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'

interface TurnstileFieldProps {
  /**
   * The action this form's tokens are minted under; the hub refuses
   * tokens whose action does not match the protected procedure.
   */
  action: string
  /** Receives the single-use Turnstile token once solved, null otherwise. */
  onPayload: (payload: string | null) => void
  ref?: (handle: CaptchaFieldHandle) => void
}

// Cloudflare Turnstile, rendered explicitly into a container div with the
// hub's site key. A solved token is single-use and expires after five
// minutes; on expiry the widget refreshes itself and the new token
// replaces the null the expired-callback delivered, so the form blocks
// submit for exactly as long as there is no usable token.
export const TurnstileField: Component<TurnstileFieldProps> = (props) => {
  let container: HTMLDivElement | undefined
  let widgetId: string | undefined
  let disposed = false
  const [ready, setReady] = createSignal(false)
  const [loadError, setLoadError] = createSignal(false)

  const clearWidget = () => {
    if (widgetId !== undefined && window.turnstile) {
      window.turnstile.remove(widgetId)
      widgetId = undefined
    }
  }

  const arm = async () => {
    setLoadError(false)
    try {
      await loadExternalScript(TURNSTILE_SCRIPT_URL)
      if (disposed || !container || !window.turnstile)
        return
      widgetId = window.turnstile.render(container, {
        'sitekey': getCaptchaSiteKey(),
        'action': props.action,
        'callback': (token) => {
          setReady(true)
          setLoadError(false)
          props.onPayload(token)
        },
        'expired-callback': () => {
          // The token is dead; the widget refreshes itself and the next
          // callback delivers a fresh one. Until then the form must not
          // submit the stale token.
          props.onPayload(null)
        },
        'error-callback': () => {
          // The widget retries recoverable errors itself; surface the
          // state so the user is not left wondering why submit is locked.
          setLoadError(true)
          props.onPayload(null)
        },
      })
      if (disposed)
        clearWidget()
    }
    catch {
      if (!disposed)
        setLoadError(true)
    }
  }

  // A site-key rotation reaches the form as a signal change after the
  // denial-driven system-info reload. Tokens minted under the retired key
  // always fail siteverify, so re-render the widget under the new key.
  // The first run only records the initial key; arm() below handles the
  // initial render.
  createEffect((prevKey?: string) => {
    const key = getCaptchaSiteKey()
    if (prevKey !== undefined && prevKey !== key) {
      props.onPayload(null)
      setReady(false)
      clearWidget()
      void arm()
    }
    return key
  })

  onMount(() => {
    void arm()
    props.ref?.({
      reset: () => {
        props.onPayload(null)
        setReady(false)
        if (widgetId !== undefined && window.turnstile)
          window.turnstile.reset(widgetId)
      },
    })
  })

  onCleanup(() => {
    disposed = true
    clearWidget()
    props.onPayload(null)
  })

  return (
    <div class={styles.field}>
      <div ref={container} />
      <CaptchaFieldStatus ready={ready()} loadError={loadError()} />
    </div>
  )
}

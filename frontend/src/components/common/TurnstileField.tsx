import type { Component } from 'solid-js'
import type { CaptchaFieldHandle } from './CaptchaField'

import { createEffect, onMount } from 'solid-js'
import { loadExternalScript } from '~/lib/scriptLoader'
import { getCaptchaSiteKey } from '~/lib/systemInfo'
import { createCaptchaFieldBase, noteFieldArmFailure } from './captchaFieldBase'

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
  // props.onPayload is a stable callback the base only invokes from
  // user-driven callbacks and cleanup; it is not a tracked read.
  // eslint-disable-next-line solid/reactivity
  const base = createCaptchaFieldBase(props.onPayload, clearWidget)

  function clearWidget() {
    if (widgetId !== undefined && window.turnstile) {
      window.turnstile.remove(widgetId)
      widgetId = undefined
    }
  }

  const arm = async () => {
    base.setLoadError(false)
    try {
      await loadExternalScript(TURNSTILE_SCRIPT_URL)
      if (base.disposed() || !container || !window.turnstile)
        return
      widgetId = window.turnstile.render(container, {
        'sitekey': getCaptchaSiteKey(),
        'action': props.action,
        'callback': (token) => {
          base.setReady(true)
          base.setLoadError(false)
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
          base.setLoadError(true)
          props.onPayload(null)
          // An unrecoverable widget error is typically a site key the
          // snapshot no longer knows; request one deduped snapshot
          // refresh so a rotated key converges instead of dead-ending
          // behind the submit gate.
          noteFieldArmFailure()
        },
      })
      if (base.disposed())
        clearWidget()
    }
    catch {
      if (!base.disposed()) {
        base.setLoadError(true)
        // The widget script could not load — the same arm-failure
        // convergence as the error callback above.
        noteFieldArmFailure()
      }
    }
  }

  // A site-key rotation reaches the form as a signal change after the
  // denial-driven system-info reload. Tokens minted under the retired key
  // always fail siteverify, so re-render the widget under the new key.
  // The first run only records the initial key; arm() below handles the
  // initial render. onSiteKeyChange runs the callback inside its own
  // createEffect, so the reactive reads in arm() are tracked.
  // eslint-disable-next-line solid/reactivity
  base.onSiteKeyChange(() => {
    clearWidget()
    void arm()
  })

  // Re-render when the form action changes. reset() keeps the action
  // baked into the first render(); a Password/Passkey switch must mint
  // a token under the new action or the hub denies it. The first run
  // only records the action; onMount arms the widget.
  createEffect((prev?: string) => {
    const action = props.action
    if (prev !== undefined && prev !== action) {
      props.onPayload(null)
      base.setReady(false)
      clearWidget()
      void arm()
    }
    return action
  })

  onMount(() => {
    void arm()
    props.ref?.({
      reset: () => {
        props.onPayload(null)
        base.setReady(false)
        if (widgetId !== undefined && window.turnstile)
          window.turnstile.reset(widgetId)
      },
    })
  })

  return (
    <base.Field>
      <div ref={container} />
    </base.Field>
  )
}

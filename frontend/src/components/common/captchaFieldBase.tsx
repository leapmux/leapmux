import type { Component, JSX } from 'solid-js'

import { createEffect, createSignal, onCleanup } from 'solid-js'
import { getCaptchaSiteKey, refreshSnapshot } from '~/lib/systemInfo'
import * as styles from './CaptchaField.css'
import { CaptchaFieldStatus } from './CaptchaFieldStatus'

/**
 * The lifecycle skeleton every captcha provider field carries: the
 * disposed flag guarding stale async continuations, the ready/loadError
 * status signals, the cleanup that drops any held payload, and the field
 * chrome (status line under the provider's widget).
 *
 * Deliberately NOT owned here: epoch counters, timers, listener
 * registration, and catch behavior — those differ per provider (the
 * external widget self-retries where the altcha fetch does not). Pass
 * them as `onTeardown` so they run BEFORE the payload drop, matching
 * each field's original cleanup order.
 */
export interface CaptchaFieldBase {
  /** True once the component's cleanup ran; guards stale continuations. */
  disposed: () => boolean
  ready: () => boolean
  setReady: (ready: boolean) => void
  loadError: () => boolean
  setLoadError: (error: boolean) => void
  /**
   * The site-key-rotation contract shared by the external providers:
   * re-arm through `rearm` immediately when getCaptchaSiteKey() changes
   * (a rotated key retires every token minted under the old one), and
   * request one deduped snapshot refresh so a rotated or invalid key
   * converges without a manual reload. Call once from the component
   * body; the effect lives in this component's reactive scope.
   */
  onSiteKeyChange: (rearm: () => void) => void
  /** The shared chrome: the given widget markup under the status line. */
  Field: Component<{ children?: JSX.Element }>
}

export function createCaptchaFieldBase(
  onPayload: (payload: string | null) => void,
  onTeardown?: () => void,
): CaptchaFieldBase {
  let disposed = false
  const [ready, setReady] = createSignal(false)
  const [loadError, setLoadError] = createSignal(false)

  onCleanup(() => {
    disposed = true
    onTeardown?.()
    onPayload(null)
  })

  const base: CaptchaFieldBase = {
    disposed: () => disposed,
    ready,
    setReady,
    loadError,
    setLoadError,
    onSiteKeyChange: (rearm: () => void) => {
      createEffect((prevKey?: string) => {
        const key = getCaptchaSiteKey()
        if (prevKey !== undefined && prevKey !== key) {
          onPayload(null)
          setReady(false)
          rearm()
        }
        return key
      })
    },
    Field: (fieldProps: { children?: JSX.Element }) => (
      <div class={styles.field}>
        {fieldProps.children}
        <CaptchaFieldStatus ready={ready()} loadError={loadError()} />
      </div>
    ),
  }
  return base
}

/**
 * The arm-failure convergence shared by every provider field: when a
 * field cannot arm (challenge fetch rejected, widget script failed,
 * execute rejected — typically a provider switch or a rotated site key
 * the page's snapshot does not know), one deduped snapshot refresh
 * re-mounts the right field. Without it the disabled submit button can
 * never trigger the denial-driven refresh, and the form dead-ends until
 * a manual page reload.
 */
export function noteFieldArmFailure(): void {
  refreshSnapshot()
}

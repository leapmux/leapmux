import type { Page } from '@playwright/test'

// ──────────────────────────────────────────────
// Toast recording for e2e debugging
// ──────────────────────────────────────────────

export interface RecordedToast {
  message: string
  variant: string
  timestamp: number
}

/**
 * Install a toast recorder on the page.
 * This monkey-patches `window.ot.toast` so that every toast message is
 * captured in `window.__recordedToasts` for later retrieval.
 *
 * Must be called **before** navigating to the app (e.g. before loginViaUI).
 * Works across page reloads because it uses `addInitScript`.
 */
export async function installToastRecorder(page: Page) {
  await page.addInitScript(() => {
    ;(window as any).__recordedToasts = [] as RecordedToast[]

    // Intercept window.ot assignment to monkey-patch toast() and toastEl()
    let _ot: any
    Object.defineProperty(window, 'ot', {
      configurable: true,
      get() {
        return _ot
      },
      set(val: any) {
        if (val && typeof val.toast === 'function') {
          const original = val.toast
          const patched = function (message: string, title?: string, options?: any) {
            ;(window as any).__recordedToasts.push({
              message,
              variant: options?.variant ?? '',
              timestamp: Date.now(),
            })
            return original.call(val, message, title, options)
          }
          // Preserve .clear method
          patched.clear = original.clear
          val.toast = patched
        }
        if (val && typeof val.toastEl === 'function') {
          const originalEl = val.toastEl
          val.toastEl = function (element: HTMLElement, options?: any) {
            const msg = element.querySelector('.toast-message')
            ;(window as any).__recordedToasts.push({
              message: msg?.textContent ?? '',
              variant: element.getAttribute('data-variant') ?? '',
              timestamp: Date.now(),
            })
            return originalEl.call(val, element, options)
          }
        }
        _ot = val
      },
    })
  })
}

/**
 * Retrieve all toast messages recorded since the last page load or clear.
 */
export async function getRecordedToasts(page: Page): Promise<RecordedToast[]> {
  return page.evaluate(() => (window as any).__recordedToasts ?? [])
}

/**
 * Clear the recorded toast list.
 */
export async function clearRecordedToasts(page: Page) {
  await page.evaluate(() => {
    ;(window as any).__recordedToasts = []
  })
}

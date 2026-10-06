import type { Page, TestInfo } from '@playwright/test'
import { expect } from '@playwright/test'

// ──────────────────────────────────────────────
// Toast recording for e2e debugging
// ──────────────────────────────────────────────

export interface RecordedToast {
  message: string
  variant: string
  timestamp: number
}

/**
 * Install a toast recorder on the page: every toast rendered while it is
 * installed is captured in `window.__recordedToasts` for later retrieval.
 *
 * Must be called **before** navigating to the app (e.g. before loginViaUI).
 * Works across page reloads because it uses `addInitScript`.
 *
 * The page fixtures of `../fixtures.ts` and `../process-control-fixtures.ts` install it on every test page already.
 * Call it only for a page that a test creates itself. Each call adds one more init script to the page, and a shared
 * page keeps that script for the rest of the worker's run.
 *
 * Recording is done by WATCHING THE DOM, not by patching oat. The previous
 * version monkey-patched `window.ot.toast` / `window.ot.toastEl`, and recorded
 * nothing at all: the app renders through `window.ot.toast.el(...)` (see
 * src/components/common/Toast.tsx), oat installs `window.ot` in a way the
 * interposed setter never saw, and replacing `ot.toast` with a bare function
 * dropped the `.el` the app then called. Every
 * `expect(dangerToasts).toHaveLength(0)` written against it therefore passed
 * on an empty list that could never fill.
 *
 * The DOM is the one surface every toast has to reach to be a toast at all --
 * `renderToast` builds `<output data-variant=…><div class="toast-message">` and
 * hands it to oat -- so observing insertions is both simpler and immune to how
 * oat is packaged.
 *
 * Note the recorder is APPEND-ONLY and outlives each toast's ~3s on-screen
 * life, which is what makes it usable for "nothing was warned about"
 * assertions: a toast that came and went still counts.
 */
export async function installToastRecorder(page: Page) {
  await page.addInitScript(() => {
    ;(window as any).__recordedToasts = [] as RecordedToast[]

    const RECORDED_ATTR = 'data-e2e-toast-recorded'

    const record = (el: Element) => {
      if (!(el instanceof HTMLElement) || !el.hasAttribute('data-variant'))
        return
      // One toast, one entry. oat inserts the element into its own container,
      // so the observer sees it arrive twice -- once as the container's subtree
      // and once on its own -- and a doubled log reads like the app fired the
      // same toast twice.
      if (el.hasAttribute(RECORDED_ATTR))
        return
      el.setAttribute(RECORDED_ATTR, '')

      const recorded = (window as any).__recordedToasts as RecordedToast[]
      recorded.push({
        message: el.querySelector('.toast-message')?.textContent ?? el.textContent ?? '',
        variant: el.getAttribute('data-variant') ?? '',
        timestamp: Date.now(),
      })
    }

    const observer = new MutationObserver((records) => {
      for (const r of records) {
        for (const node of r.addedNodes) {
          if (!(node instanceof HTMLElement))
            continue
          // The toast may be inserted directly or inside oat's own wrapper.
          if (node.tagName === 'OUTPUT') {
            record(node)
          }
          else {
            node.querySelectorAll('output[data-variant]').forEach(record)
          }
        }
      }
    })
    // `document`, not `document.documentElement`: an init script runs before
    // any page script, which is early enough that the root element may not
    // exist yet -- and `observe(null, …)` throws, taking the whole recorder
    // down silently while `__recordedToasts` sits there as a plausible empty
    // array. `document` is always present and subtree:true reaches the same
    // nodes.
    observer.observe(document, { childList: true, subtree: true })
  })
}

/**
 * Retrieve all toast messages recorded since the last page load or clear.
 */
export async function getRecordedToasts(page: Page): Promise<RecordedToast[]> {
  return page.evaluate(() => (window as any).__recordedToasts ?? [])
}

/**
 * Wait until the recorder holds a toast whose message contains `text`, or matches it.
 *
 * Use this, not a locator on the visible toast: a toast shows for about 3 seconds, and the recorder keeps it after
 * it leaves, so the check cannot miss a toast that came and went before the assertion ran.
 * The recorder holds every toast since the last page load or `clearRecordedToasts`. Clear it before the action
 * when an earlier toast could also match.
 */
export async function expectToastRecorded(page: Page, text: string | RegExp): Promise<void> {
  if (text === '')
    throw new Error('A toast check needs text, because every toast contains an empty text.')
  const matcher = typeof text === 'string' ? expect.stringContaining(text) : expect.stringMatching(text)
  await expect.poll(async () => (await getRecordedToasts(page)).map(toast => toast.message), {
    message: `the page shows a toast that holds ${typeof text === 'string' ? JSON.stringify(text) : String(text)}`,
  }).toContainEqual(matcher)
}

/** The recorded toasts of the danger variant, which the app uses for an error. */
export async function dangerToasts(page: Page): Promise<RecordedToast[]> {
  return (await getRecordedToasts(page)).filter(toast => toast.variant === 'danger')
}

/**
 * Format the recorded toasts with one line for each toast: `[<ISO time>] [<variant>] <message>`.
 * A toast with no variant reads as `info`.
 */
export function formatToastLog(toasts: readonly RecordedToast[]): string {
  return toasts.map(toast => `[${new Date(toast.timestamp).toISOString()}] [${toast.variant || 'info'}] ${toast.message}`).join('\n')
}

/**
 * Attach the toasts that the page recorded to the test report as `toast-log`.
 * The function attaches nothing when the page recorded no toast. It also attaches nothing when the page cannot answer,
 * as after a crash or a close, because a missing toast log must not hide the failure of the test.
 */
export async function attachToastLog(page: Page, testInfo: TestInfo): Promise<void> {
  const toasts = await getRecordedToasts(page).catch(() => [])
  if (toasts.length === 0)
    return
  await testInfo.attach('toast-log', { body: formatToastLog(toasts), contentType: 'text/plain' })
}

/**
 * Clear the recorded toast list.
 */
export async function clearRecordedToasts(page: Page) {
  await page.evaluate(() => {
    ;(window as any).__recordedToasts = []
  })
}

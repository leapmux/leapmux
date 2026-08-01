import type { Page } from '@playwright/test'

import { expect } from '@playwright/test'
import { setInitialBrowserPref } from './ui'

/** Distinctive fragment of the bundled turn-end sound's asset path. */
const DOORBELL_SRC = 'benkirb-electronic-doorbell'

/**
 * How long a "no further ding arrives" assertion stays quiet before it is
 * believed.
 *
 * A negative assertion about an event has no positive edge to wait on, so a
 * settle is unavoidable here -- but it is the ONLY place these specs sleep.
 * Every wait for a ding that IS expected polls instead (see
 * {@link expectDoorbellCount}), because there the arrival is observable.
 */
const QUIET_SETTLE_MS = 1000

/**
 * Record every `HTMLAudioElement.play()` call, pin the browser-level turn-end
 * sound to `sound`, then reload so both take effect.
 *
 * The spy has to be installed via `addInitScript` (it patches a prototype
 * before the app's first render) and the preference has to be written before
 * the app boots, which is why arming is a reload rather than an in-page call.
 * Callers wait for their own readiness signal afterwards -- what "ready" means
 * differs between the workspace specs and the provider ones.
 */
export async function armTurnEndSound(page: Page, sound: 'ding-dong' | 'none') {
  await page.addInitScript(() => {
    (window as any).__audioPlayCalls = [] as string[]
    HTMLAudioElement.prototype.play = function () {
      (window as any).__audioPlayCalls.push(this.src)
      return Promise.resolve()
    }
  })
  await setInitialBrowserPref(page, 'turnEndSound', sound)
  await page.reload()
}

/** How many times the turn-end doorbell has played on this page so far. */
export async function doorbellCount(page: Page): Promise<number> {
  return page.evaluate(
    src => ((window as any).__audioPlayCalls as string[]).filter(s => s.includes(src)).length,
    DOORBELL_SRC,
  )
}

/**
 * Wait until the doorbell has played exactly `count` times.
 *
 * Polls rather than sleeping after the turn appears to end, because "the turn
 * ended" and "the ding fired" are driven by DIFFERENT events: the interrupt
 * button hides when `agentWorking` goes false (or a control request opens),
 * while the sound fires from the result divider in `handleResultDivider`. The
 * gap between them is whatever the event stream and the render loop cost,
 * which a loaded machine widens without bound -- so the fixed 200-500ms sleeps
 * these specs used to take were a bet on that gap, and lost it under the full
 * suite's concurrency.
 */
export async function expectDoorbellCount(page: Page, count: number) {
  await expect.poll(() => doorbellCount(page)).toBe(count)
}

/**
 * Assert the doorbell count stays at `count` through a quiet period -- i.e.
 * that whatever just happened did NOT ring it again.
 */
export async function expectDoorbellQuiet(page: Page, count: number) {
  await page.waitForTimeout(QUIET_SETTLE_MS)
  expect(await doorbellCount(page)).toBe(count)
}

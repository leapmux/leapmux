import type { Page } from '@playwright/test'
import { uniqueMarker } from './shellArguments'

/**
 * Samples of a page at every moment of an action, for a check that the page never showed a wrong state while the
 * action ran, not even for one frame.
 *
 * A retrying assertion after the action sees only the final state. The sampler runs on every DOM change and every
 * scroll, in the same task as the change, so a state that the page showed and then replaced still leaves a sample.
 */

/** The record that the sampler keeps on `window`, under its own key. */
interface DomSampleRecord<T> {
  samples: T[]
  stop: () => void
}

/**
 * Take a sample with `sample` now, after every DOM change, and on every scroll event in the document, and keep each
 * one in `window[key].samples` until `window[key].stop()` runs. A scroll event of an element does not bubble, so the
 * sampler listens in the capture phase.
 *
 * This function runs in the page as source text, so it uses nothing from its module scope, and neither does `sample`.
 */
export function installDomSampler<T>(sample: () => T, key: string): void {
  const samples: T[] = []
  const take = () => {
    samples.push(sample())
  }
  const observer = new MutationObserver(take)
  observer.observe(document, {
    subtree: true,
    childList: true,
    attributes: true,
    characterData: true,
  })
  document.addEventListener('scroll', take, true)
  const record: DomSampleRecord<T> = {
    samples,
    stop() {
      observer.disconnect()
      document.removeEventListener('scroll', take, true)
    },
  }
  Object.defineProperty(window, key, { value: record, configurable: true })
  take()
}

/**
 * Stop the sampler under `key`, remove it from `window`, and return its samples, or undefined when the page holds no
 * sampler under `key`.
 *
 * This function runs in the page, so it uses nothing from its module scope.
 */
export function finishDomSampler(key: string): unknown[] | undefined {
  const record: unknown = Reflect.get(window, key)
  if (typeof record !== 'object' || record === null)
    return undefined
  Reflect.deleteProperty(window, key)
  const sampler = record as DomSampleRecord<unknown>
  sampler.stop()
  return sampler.samples
}

/**
 * Run `act`, and return every sample that `sample` took in the current document of `page`: one before `act`, one
 * after each DOM change, and one on each scroll event, until `act` returned.
 *
 * `sample` runs in the page as source text, so it uses nothing from its module scope, and it returns a value that
 * survives the trip to the test as JSON. The sampler stops when `act` returns or fails, so it never outlives the
 * action on the page that the next tests share.
 */
export async function domSamplesDuring<T>(page: Page, sample: () => T, act: () => Promise<void>): Promise<T[]> {
  const key = `__e2eDomSamples_${uniqueMarker()}`
  await page.evaluate(`(${installDomSampler.toString()})(${sample.toString()}, ${JSON.stringify(key)})`)
  let samples: unknown[] | undefined
  try {
    await act()
  }
  finally {
    samples = await page.evaluate(finishDomSampler, key)
  }
  if (samples === undefined)
    throw new TypeError('The page holds no samples: the action replaced the document that the sampler watched.')
  return samples as T[]
}

import type { Page } from '@playwright/test'
import { randomUUID } from 'node:crypto'

/**
 * The largest number of elements that a reader could see at one moment of a navigation, for a check that a page
 * never showed a stale state while it loaded, not even for one frame.
 *
 * A retrying assertion after the load sees only the final state, and a wait before it sees one more moment. The
 * recorder sees every DOM change from the first byte of the new document, so a stale state that the page showed and
 * then replaced still counts.
 */

/** Where the recorder counts: the visible `item` elements inside each element that matches `container`. */
export interface VisibleCountScope {
  container: string
  item: string
}

/** The record that the recorder keeps on `window`, under its own key. */
interface VisibleCountRecord {
  max: number
}

/**
 * Count, on every DOM change from now on, the visible `scope.item` elements inside each `scope.container` element,
 * and keep the largest count of one container in `window[key].max`.
 *
 * The count is per container, because the app mounts some parts twice (the desktop and the mobile sidebar), and both
 * copies can show for a moment during a layout change. Each copy then shows its own items once, which is no stale
 * state.
 *
 * "Visible" is `checkVisibility` with the `visibility` property: an element in a `display: none` subtree, or under
 * `visibility: hidden`, as the shell is while the boot splash covers it, does not count. The recorder runs before the
 * document has a root element, so it observes the document itself.
 *
 * This function runs in the page as the source text of an init script, so it uses nothing from its module scope.
 */
export function installVisibleCountRecorder(scope: VisibleCountScope, key: string): void {
  const record: VisibleCountRecord = { max: 0 }
  Object.defineProperty(window, key, { value: record, configurable: true })
  const visible = (element: Element) => element.checkVisibility({ visibilityProperty: true })
  const count = () => {
    for (const container of document.querySelectorAll(scope.container)) {
      if (!visible(container))
        continue
      let items = 0
      for (const item of container.querySelectorAll(scope.item)) {
        if (visible(item))
          items++
      }
      record.max = Math.max(record.max, items)
    }
  }
  new MutationObserver(count).observe(document, { subtree: true, childList: true, attributes: true, characterData: true })
  count()
}

/**
 * Run `navigate`, which reloads or navigates `page` and then waits for the end of the load, and return the largest
 * number of visible `scope.item` elements that one `scope.container` element held at any moment of the new document
 * until `navigate` returned.
 *
 * The recorder is an init script that the helper adds through the DevTools protocol and removes again after
 * `navigate`, also after a failure. `page.addInitScript` is not usable here: no API removes it, so it would stay on
 * the page that the next tests share.
 */
export async function maxVisibleCountDuring(page: Page, scope: VisibleCountScope, navigate: () => Promise<void>): Promise<number> {
  if (scope.container.trim() === '' || scope.item.trim() === '')
    throw new Error('A visible count needs a container selector and an item selector.')
  const key = `__e2eVisibleCount_${randomUUID().replaceAll('-', '')}`
  const source = `(${installVisibleCountRecorder.toString()})(${JSON.stringify(scope)}, ${JSON.stringify(key)})`
  const cdp = await page.context().newCDPSession(page)
  try {
    // Chromium runs a script that a session adds only when that session enabled its Page domain. A new session starts
    // with the domain off, so without this call the reload runs no recorder.
    await cdp.send('Page.enable')
    const { identifier } = await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source })
    try {
      await navigate()
      return await page.evaluate((name) => {
        const record: unknown = Reflect.get(window, name)
        const max: unknown = typeof record === 'object' && record !== null ? Reflect.get(record, 'max') : undefined
        if (typeof max !== 'number')
          throw new Error('The page holds no visible count: the navigation did not start a new document after the recorder was added.')
        return max
      }, key)
    }
    finally {
      await cdp.send('Page.removeScriptToEvaluateOnNewDocument', { identifier })
    }
  }
  finally {
    await cdp.detach()
  }
}

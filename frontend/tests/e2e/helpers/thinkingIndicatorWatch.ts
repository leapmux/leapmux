import type { Page } from '@playwright/test'
import { withCleanup } from './cleanup'

declare global {
  interface Window {
    __thinkingIndicatorWatch?: { shown: boolean, stop: () => void }
  }
}

/**
 * Start a watch in the page that records whether a thinking indicator shows, even for one frame.
 *
 * ChatView keeps each ThinkingIndicator in the DOM at all times (`src/components/chat/widgets/ThinkingIndicator.tsx`).
 * A hidden indicator has the inline style `display: none`, and a collapsed one has `grid-template-rows: 0fr`. So the
 * presence of the element proves nothing. The watch counts an indicator as shown only while its inline style states
 * a `display` other than `none` and `grid-template-rows: 1fr`. It checks at install, and again at each change of the
 * tree or of a `style` attribute.
 *
 * The page runs this function, so its body must not use a name from outside itself.
 */
export function installThinkingIndicatorWatch(): void {
  window.__thinkingIndicatorWatch?.stop()
  const observer = new MutationObserver(() => inspect())
  const watch = { shown: false, stop: () => observer.disconnect() }
  function inspect(): void {
    for (const element of document.querySelectorAll<HTMLElement>('[data-testid="thinking-indicator"]')) {
      if (element.style.display !== 'none' && element.style.gridTemplateRows === '1fr') {
        watch.shown = true
        watch.stop()
        return
      }
    }
  }
  window.__thinkingIndicatorWatch = watch
  observer.observe(document.documentElement, { childList: true, subtree: true, attributes: true, attributeFilter: ['style'] })
  inspect()
}

/** Stop the watch, remove it from the page, and return whether an indicator showed. A reload removes the watch. */
function stopThinkingIndicatorWatch(): boolean {
  const watch = window.__thinkingIndicatorWatch
  if (!watch)
    throw new Error('The thinking-indicator watch is not in the page. A reload during the operation removes it.')
  watch.stop()
  delete window.__thinkingIndicatorWatch
  return watch.shown
}

/**
 * Run `operation` under a thinking-indicator watch, and return whether an indicator showed at any time during it.
 * The watch stops after the operation, also when the operation fails. A failed operation keeps its own error.
 */
export async function thinkingIndicatorShownDuring(page: Pick<Page, 'evaluate'>, operation: () => Promise<void>): Promise<boolean> {
  await page.evaluate(installThinkingIndicatorWatch)
  let shown = false
  await withCleanup(operation, async () => {
    shown = await page.evaluate(stopThinkingIndicatorWatch)
  })
  return shown
}

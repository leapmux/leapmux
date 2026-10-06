import type { Page } from '@playwright/test'

/**
 * The text that the page has selected, read from the RANGE rather than from the selection. An empty or collapsed
 * selection reads as the empty string.
 *
 * `Selection.toString()` is layout-aware and reads empty over `user-select: none`. A coarse pointer puts that style
 * on every chat row, and the app puts it back the moment a finger lands away from the highlight. So on a phone the
 * selection reports "nothing is selected" while the range is still live. Every guard in the app asks the range (see
 * `selectionInside` in `~/lib/textSelection.ts`), and so must a spec, or the spec passes on a selection that never
 * went away. On a fine pointer the two reads agree.
 */
export async function selectedText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const selection = window.getSelection()
    if (!selection || selection.rangeCount === 0 || selection.isCollapsed)
      return ''
    return selection.getRangeAt(0).toString()
  })
}

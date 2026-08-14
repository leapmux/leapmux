/**
 * Select an element's text and give the range one client rect.
 *
 * jsdom does no layout, so `Range.getClientRects` returns an empty list unless
 * it is supplied -- and every hit-test against a selection (a right-click that
 * must yield to the browser's Copy) reads that empty list as "nothing
 * selected". This helper installs one synthetic rect so those predicates have
 * geometry to answer with.
 */
export function selectTextWithRect(el: HTMLElement, rect: { left: number, right: number, top: number, bottom: number }) {
  const range = document.createRange()
  range.selectNodeContents(el)
  const selection = window.getSelection()!
  selection.removeAllRanges()
  selection.addRange(range)
  const live = selection.getRangeAt(0) as Range & { getClientRects: () => DOMRectList }
  live.getClientRects = () =>
    [{ ...rect, width: rect.right - rect.left, height: rect.bottom - rect.top }] as unknown as DOMRectList
}

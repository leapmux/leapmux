import { limitTextForDisplay } from '../../../src/components/chat/safeTextDisplay'

export type DeepseekHarnessRenderedMcpBlock = { type: 'text', text: string } | { type: 'image', index: number }

/** Project the current native fixture text into its expected display blocks. */
export function deepseekHarnessMcpTextDisplay(text: string): DeepseekHarnessRenderedMcpBlock[] {
  return text ? [{ type: 'text', text: limitTextForDisplay(text).text }] : []
}

/** Read visible Model Context Protocol (MCP) result blocks in DOM order. */
export function deepseekHarnessRenderedMcpContent(roots: Element | readonly Element[], displayNotices: readonly string[]): DeepseekHarnessRenderedMcpBlock[] | null {
  // Playwright serializes this function. Keep every runtime dependency inside it or in its arguments.
  const root = roots instanceof Element ? roots : roots.find(element => element.isConnected)
  if (!root?.isConnected)
    return null
  const content: DeepseekHarnessRenderedMcpBlock[] = []
  let imageIndex = 0
  for (const element of root.querySelectorAll<HTMLElement>('p, [data-large-text-display], button[aria-label="Open image"] img')) {
    if (!element.isConnected || element.getClientRects().length === 0)
      continue
    const style = getComputedStyle(element)
    if (style.visibility === 'hidden' || style.visibility === 'collapse')
      continue
    let hidden = false
    for (let ancestor: Element | null = element; ancestor; ancestor = ancestor.parentElement) {
      if (ancestor.hasAttribute('hidden') || ancestor.getAttribute('aria-hidden') === 'true' || getComputedStyle(ancestor).display === 'none') {
        hidden = true
        break
      }
    }
    if (hidden)
      continue
    if (element.tagName === 'IMG') {
      content.push({ type: 'image', index: imageIndex++ })
      continue
    }
    const text = element.textContent ?? ''
    if (element.tagName === 'P' && element.previousElementSibling?.hasAttribute('data-large-text-display') && displayNotices.includes(text))
      continue
    if (text !== '')
      content.push({ type: 'text', text })
  }
  return content
}

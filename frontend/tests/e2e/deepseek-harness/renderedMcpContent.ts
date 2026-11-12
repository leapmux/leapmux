import { createMarkdownParser } from '../../../src/lib/markdownParse'

export type DeepseekHarnessRenderedMcpBlock = { type: 'text', text: string } | { type: 'image', index: number }

/**
 * Project one native text block of the controlled fixture into its expected visible occurrences.
 * The shared Markdown parser shows one occurrence for each paragraph, whatever the size of the text.
 * This oracle accepts only paragraphs of plain text. It refuses other Markdown so that it never drops text.
 */
export function deepseekHarnessMcpTextDisplay(text: string): DeepseekHarnessRenderedMcpBlock[] {
  if (!text)
    return []
  return createMarkdownParser().parse(text).children.map((node) => {
    if (node.type !== 'paragraph')
      throw new Error(`The DeepSeek Harness fixture text contains an unsupported Markdown ${node.type} block.`)
    return {
      type: 'text',
      text: node.children.map((child) => {
        if (child.type !== 'text')
          throw new Error(`The DeepSeek Harness fixture text contains unsupported inline Markdown ${child.type}.`)
        return child.value
      }).join(''),
    }
  })
}

/** Read visible Model Context Protocol (MCP) result blocks in DOM order. */
export function deepseekHarnessRenderedMcpContent(roots: Element | readonly Element[]): DeepseekHarnessRenderedMcpBlock[] | null {
  // Playwright serializes this function. Keep every runtime dependency inside it or in its arguments.
  const root = roots instanceof Element
    ? roots
    : roots.find(element => element.isConnected)
  if (!root?.isConnected)
    return null
  const content: DeepseekHarnessRenderedMcpBlock[] = []
  let imageIndex = 0
  for (const element of root.querySelectorAll<HTMLElement>('p, button[aria-label="Open image"] img')) {
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
    if (text !== '')
      content.push({ type: 'text', text })
  }
  return content
}

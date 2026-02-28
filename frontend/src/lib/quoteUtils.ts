/**
 * Format a file quote with path, line range, and selected text in a fenced code block blockquote.
 */
export function formatFileQuote(filePath: string, startLine: number, endLine: number, selectedText: string): string {
  const lineRef = startLine === endLine ? `Line ${startLine}` : `Line ${startLine}-${endLine}`
  const quotedLines = selectedText.split('\n').map(line => `> ${line}`)
  return `From @${filePath} (${lineRef}):\n\n> \`\`\`\n${quotedLines.join('\n')}\n> \`\`\``
}

/**
 * Format chat text as a blockquote (each line prefixed with "> ").
 */
export function formatChatQuote(text: string): string {
  return `${text.split('\n').map(line => `> ${line}`).join('\n')}\n\n`
}

/**
 * Format a file path as an @mention.
 */
export function formatFileMention(relativePath: string): string {
  return `@${relativePath}`
}

/**
 * Extract markdown-like text from a DOM selection, preserving inline formatting.
 * Falls back to plain selection.toString() if no range is available.
 */
export function extractSelectionMarkdown(selection: Selection): string {
  if (selection.rangeCount === 0)
    return selection.toString()
  const range = selection.getRangeAt(0)
  const fragment = range.cloneContents()
  return nodeToMarkdown(fragment).trim()
}

/** Convert a DOM node tree to a markdown string, preserving inline formatting. */
export function nodeToMarkdown(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE)
    return node.textContent ?? ''

  if (!(node instanceof HTMLElement) && !(node instanceof DocumentFragment))
    return node.textContent ?? ''

  const tag = node instanceof HTMLElement ? node.tagName.toLowerCase() : ''
  const childMd = () => Array.from(node.childNodes).map(nodeToMarkdown).join('')

  switch (tag) {
    case 'strong':
    case 'b':
      return `**${childMd()}**`
    case 'em':
    case 'i':
      return `*${childMd()}*`
    case 'code': {
      // Inline code (not inside <pre>)
      const text = node.textContent ?? ''
      return `\`${text}\``
    }
    case 'pre': {
      // Code block — extract language hint from <code> child class
      const codeChild = node.querySelector('code')
      const text = codeChild?.textContent ?? node.textContent ?? ''
      let lang = ''
      if (codeChild) {
        const cls = Array.from(codeChild.classList).find(c => c.startsWith('language-'))
        if (cls)
          lang = cls.slice('language-'.length)
      }
      return `\n\`\`\`${lang}\n${text}\n\`\`\`\n`
    }
    case 'a': {
      const href = (node as HTMLAnchorElement).getAttribute('href') ?? ''
      return href ? `[${childMd()}](${href})` : childMd()
    }
    case 'br':
      return '\n'
    case 'p':
      return `${childMd()}\n\n`
    case 'blockquote': {
      const inner = childMd().replace(/\n+$/, '')
      return `${inner.split('\n').map(line => `> ${line}`).join('\n')}\n`
    }
    case 'ul':
    case 'ol':
      return `${childMd()}\n`
    case 'li': {
      const parent = node instanceof HTMLElement ? node.parentElement : null
      if (parent?.tagName.toLowerCase() === 'ol') {
        const idx = Array.from(parent.children).indexOf(node as HTMLElement) + 1
        return `${idx}. ${childMd().trim()}\n`
      }
      return `- ${childMd().trim()}\n`
    }
    case 'h1': return `# ${childMd()}\n\n`
    case 'h2': return `## ${childMd()}\n\n`
    case 'h3': return `### ${childMd()}\n\n`
    case 'h4': return `#### ${childMd()}\n\n`
    case 'h5': return `##### ${childMd()}\n\n`
    case 'h6': return `###### ${childMd()}\n\n`
    case 'hr': return '\n---\n'
    case 'del':
    case 's':
      return `~~${childMd()}~~`
    case 'div':
      return `${childMd()}\n`
    default:
      return childMd()
  }
}

/**
 * Extract line range from a DOM selection by walking up to find data-line-num attributes.
 */
export function extractLineRange(selection: Selection): { startLine: number, endLine: number } | null {
  const anchorLine = findLineNum(selection.anchorNode)
  const focusLine = findLineNum(selection.focusNode)
  if (anchorLine == null || focusLine == null)
    return null
  const startLine = Math.min(anchorLine, focusLine)
  const endLine = Math.max(anchorLine, focusLine)
  return { startLine, endLine }
}

function findLineNum(node: Node | null): number | null {
  let current: Node | null = node
  while (current) {
    if (current instanceof HTMLElement) {
      const attr = current.getAttribute('data-line-num')
      if (attr != null) {
        const num = Number.parseInt(attr, 10)
        if (!Number.isNaN(num))
          return num
      }
    }
    current = current.parentElement
  }
  return null
}

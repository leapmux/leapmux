/**
 * Format a file quote with path, line range, and selected text in a fenced code block blockquote.
 */
export function formatFileQuote(filePath: string, startLine: number, endLine: number, selectedText: string): string {
  const lineRef = startLine === endLine ? `:${startLine}` : `:${startLine}-${endLine}`
  const quotedLines = selectedText.split('\n').map(line => `> ${line}`)
  return `At ${filePath}${lineRef}\n\n> \`\`\`\n${quotedLines.join('\n')}\n> \`\`\``
}

/**
 * Format chat text as a blockquote (each line prefixed with "> ").
 */
export function formatChatQuote(text: string): string {
  return text.split('\n').map(line => `> ${line}`).join('\n')
}

/**
 * Format a file path as an @mention.
 */
export function formatFileMention(relativePath: string): string {
  return `@${relativePath}`
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

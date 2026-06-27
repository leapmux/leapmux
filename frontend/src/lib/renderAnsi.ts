import { shikiHighlighter } from './renderMarkdown'

const ESC = '\x1B'
const BEL = '\x07'

export function containsAnsi(text: string): boolean {
  let index = text.indexOf(ESC)
  while (index !== -1) {
    const introducer = text[index + 1]
    if (introducer === '[' || introducer === ']' || isSingleCharEscapeFinal(introducer))
      return true
    index = text.indexOf(ESC, index + 1)
  }
  return false
}

function skipCsi(text: string, index: number): number {
  for (let i = index; i < text.length; i++) {
    const code = text.charCodeAt(i)
    if (code >= 0x40 && code <= 0x7E)
      return i + 1
  }
  return text.length
}

function skipOsc(text: string, index: number): number {
  for (let i = index; i < text.length; i++) {
    if (text[i] === BEL)
      return i + 1
    if (text[i] === ESC && text[i + 1] === '\\')
      return i + 2
  }
  return text.length
}

function isSingleCharEscapeFinal(char: string | undefined): boolean {
  if (char === undefined)
    return false
  const code = char.charCodeAt(0)
  return code >= 0x40 && code <= 0x5F
}

function stripUnsupportedAnsiControls(text: string): string {
  let result = ''
  for (let i = 0; i < text.length;) {
    if (text[i] !== ESC) {
      result += text[i]
      i++
      continue
    }

    const introducer = text[i + 1]
    if (introducer === '[') {
      const end = skipCsi(text, i + 2)
      if (text[end - 1] === 'm')
        result += text.slice(i, end)
      i = end
      continue
    }
    if (introducer === ']') {
      i = skipOsc(text, i + 2)
      continue
    }
    if (isSingleCharEscapeFinal(introducer)) {
      i += 2
      continue
    }

    i++
  }
  return result
}

/** Remove ANSI control escapes while preserving the visible text payload. */
export function stripAnsi(text: string): string {
  let result = ''
  for (let i = 0; i < text.length;) {
    if (text[i] !== ESC) {
      result += text[i]
      i++
      continue
    }

    const introducer = text[i + 1]
    if (introducer === '[') {
      i = skipCsi(text, i + 2)
      continue
    }
    if (introducer === ']') {
      i = skipOsc(text, i + 2)
      continue
    }
    if (isSingleCharEscapeFinal(introducer)) {
      i += 2
      continue
    }

    result += text[i]
    i++
  }
  return result
}

/**
 * Render text containing ANSI escape sequences to themed HTML using Shiki.
 *
 * Produces a `<pre class="shiki ..."><code>...</code></pre>` structure with
 * CSS variable-based dual-theme coloring (--shiki-light / --shiki-dark).
 */
export function renderAnsi(text: string): string {
  const sanitized = stripUnsupportedAnsiControls(text)
  try {
    return shikiHighlighter.codeToHtml(sanitized, {
      lang: 'ansi',
      themes: { light: 'github-light', dark: 'github-dark' },
      defaultColor: false,
    })
  }
  catch {
    return `<pre><code>${escapeHtml(stripAnsi(sanitized))}</code></pre>`
  }
}

const RE_AMP = /&/g
const RE_LT = /</g
const RE_GT = />/g

export function escapeHtml(s: string): string {
  return s.replace(RE_AMP, '&amp;').replace(RE_LT, '&lt;').replace(RE_GT, '&gt;')
}

import { shikiHighlighter } from './renderMarkdown'

/**
 * Regex to detect the presence of ANSI SGR (Select Graphic Rendition) escape sequences.
 * Matches ESC[ ... m sequences used for colors, bold, underline, etc.
 */
// eslint-disable-next-line no-control-regex -- ANSI escape detection requires matching control characters
const ANSI_REGEX = /\x1B\[[\d;]*m/

/** Returns true if the text contains at least one ANSI escape sequence. */
export function containsAnsi(text: string): boolean {
  return ANSI_REGEX.test(text)
}

/**
 * Render text containing ANSI escape sequences to themed HTML using Shiki.
 *
 * Produces a `<pre class="shiki ..."><code>...</code></pre>` structure with
 * CSS variable-based dual-theme coloring (--shiki-light / --shiki-dark).
 */
export function renderAnsi(text: string): string {
  return shikiHighlighter.codeToHtml(text, {
    lang: 'ansi',
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
  })
}

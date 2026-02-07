import type { JSX } from 'solid-js'
import type { CachedToken } from '~/lib/tokenCache'
import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import { guessLanguage } from '~/lib/languageMap'
import { tokenizeAsync } from '~/lib/shikiWorkerClient'
import { getCachedTokens } from '~/lib/tokenCache'
import {
  codeViewContainer,
  codeViewContent,
  codeViewLine,
  codeViewLineNumber,
} from './codeViewStyles.css'

/** A single parsed line from Read tool output. */
export interface ParsedCatLine {
  num: number
  text: string
}

/** Regex to match a single Read output line: optional whitespace, digits, →, content. */
const CAT_N_LINE_RE = /^\s*(\d+)→(.*)$/

/** Skip syntax highlighting for files above this many lines. */
const HIGHLIGHT_LINE_LIMIT = 1000

/**
 * Parse Read tool output content into structured lines.
 * Each line follows: optional whitespace + digits + → (U+2192) + content.
 * Returns null if the content does not match the expected format.
 */
export function parseCatNContent(content: string): ParsedCatLine[] | null {
  if (!content)
    return null
  const rawLines = content.split('\n')
  // Tolerate trailing empty line
  if (rawLines.length > 0 && rawLines[rawLines.length - 1] === '')
    rawLines.pop()
  if (rawLines.length === 0)
    return null

  const parsed: ParsedCatLine[] = []
  for (const raw of rawLines) {
    const m = raw.match(CAT_N_LINE_RE)
    if (!m)
      return null
    parsed.push({ num: Number.parseInt(m[1], 10), text: m[2] })
  }
  return parsed
}

/**
 * Syntax-highlighted code view for Read tool results.
 * Renders line numbers alongside Shiki-highlighted code content.
 * Tokenization runs in a Web Worker to avoid blocking the main thread.
 */
export function ReadResultView(props: {
  lines: ParsedCatLine[]
  filePath?: string
}): JSX.Element {
  const lang = () => props.filePath ? guessLanguage(props.filePath) : undefined

  const [tokenizedLines, setTokenizedLines] = createSignal<CachedToken[][] | null>(null)

  createEffect(on(
    () => [lang(), props.lines] as const,
    ([l, lines]) => {
      setTokenizedLines(null)

      if (!l || lines.length === 0 || lines.length > HIGHLIGHT_LINE_LIMIT)
        return

      const code = lines.map(line => line.text).join('\n')

      // Synchronous cache check — avoids flash of unstyled text on re-expand
      const cached = getCachedTokens(l, code)
      if (cached) {
        setTokenizedLines(cached)
        return
      }

      // Async: dispatch to Web Worker, render plain text until ready
      let cancelled = false
      tokenizeAsync(l, code).then((tokens) => {
        if (!cancelled) {
          setTokenizedLines(tokens)
        }
      })

      onCleanup(() => {
        cancelled = true
      })
    },
  ))

  // Dynamic line number column width based on the largest line number
  const lineNumWidth = () => {
    const maxNum = props.lines.length > 0
      ? props.lines[props.lines.length - 1].num
      : 0
    return `${Math.max(String(maxNum).length, 1)}ch`
  }

  return (
    <div class={codeViewContainer}>
      <For each={props.lines}>
        {(line, index) => {
          const tokens = () => {
            const t = tokenizedLines()
            return t?.[index()] ?? null
          }
          return (
            <div class={codeViewLine}>
              <span
                class={codeViewLineNumber}
                style={{ width: lineNumWidth() }}
              >
                {line.num}
              </span>
              <span class={codeViewContent}>
                <Show
                  when={tokens()}
                  fallback={line.text}
                >
                  <For each={tokens()!}>
                    {token => (
                      <span style={token.htmlStyle as JSX.CSSProperties}>{token.content}</span>
                    )}
                  </For>
                </Show>
              </span>
            </div>
          )
        }}
      </For>
    </div>
  )
}

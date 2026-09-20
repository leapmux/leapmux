import type { JSX } from 'solid-js'
import type { NumberedFileLine } from '../model/readFileResult'
import { createMemo, For, Show } from 'solid-js'
import { ansiSyncTokenize } from '~/lib/ansiTokenize'
import { guessLanguage } from '~/lib/languageMap'
import {
  codeViewContainer,
  codeViewContent,
  codeViewLine,
  codeViewLineNumber,
} from '../markdownEditor/codeViewStyles.css'
import { useAsyncCodeTokens } from '../useAsyncCodeTokens'
import { HIGHLIGHT_LINE_LIMIT } from './collapse'

/**
 * Syntax-highlighted code view for Read tool results.
 * Renders line numbers alongside Shiki-highlighted code content.
 * Tokenization runs in a Web Worker to avoid blocking the main thread.
 */
export function ReadResultView(props: {
  lines: NumberedFileLine[]
  filePath?: string
  /** Hidden DOM premeasurement keeps line geometry but skips Shiki/token workers. */
  premeasureMode?: boolean
  /** Visible scrolling keeps line geometry but skips Shiki/token workers until idle. */
  syntaxHighlightingPaused?: boolean
  /** Active browser selection: keep existing tokens but avoid replacing text nodes. */
  textSelectionActive?: () => boolean
  /** Row outside the near-viewport band: tokenize at low worker priority. */
  rowOffscreen?: () => boolean
}): JSX.Element {
  // Memoized (like useDiffTokens' `lang`) so the hook's 2-4 reads per reactive pass --
  // both effects' currentKey(), the seed, and syncTokenize -- don't each re-run extname()
  // + the EXT_TO_LANG lookup.
  const lang = createMemo(() => props.filePath ? guessLanguage(props.filePath) : undefined)
  // Memoized so the O(lines) join isn't rebuilt on every read -- the hook reads `code`
  // from both effects' currentKey() plus syncTokenize (2-3x per reactive pass).
  const code = createMemo(() => props.lines.map(line => line.text).join('\n'))

  const tokenizedLines = useAsyncCodeTokens({
    lang,
    code,
    // Line-count gate (very large files skip highlighting); lang presence is checked
    // by the hook before this runs.
    eligible: () => props.lines.length > 0 && props.lines.length <= HIGHLIGHT_LINE_LIMIT,
    gate: () => ({
      premeasure: props.premeasureMode === true,
      hold: props.syntaxHighlightingPaused === true || props.textSelectionActive?.() === true,
    }),
    // ANSI is a special Shiki built-in -- tokenize synchronously on the main thread
    // (the worker's Oniguruma core has no `ansi` grammar). null falls through to the
    // worker, which renders it plain. Shared with the diff sides / gap-context lines so
    // a `.log` file highlights identically wherever it appears.
    syncTokenize: ansiSyncTokenize,
    rowOffscreen: () => props.rowOffscreen?.() === true,
  })

  // Dynamic line number column width based on the largest line number
  const lineNumWidth = createMemo(() => {
    const maxNum = props.lines.length > 0
      ? props.lines.at(-1)!.num
      : 0
    return `${Math.max(String(maxNum).length, 1)}ch`
  })

  return (
    <div class={codeViewContainer}>
      <For each={props.lines}>
        {(line, index) => {
          const tokens = () => {
            const t = tokenizedLines()
            return t?.[index()] ?? null
          }
          return (
            <div class={codeViewLine} data-line-num={line.num}>
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
                      // `data-shiki-token` marks THIS as a syntax token so the dual-theme
                      // color rule targets it precisely -- not the line-number span. The
                      // token's style lives in the shared class (shikiStyleClass). Mirrors
                      // the Bash/JSON token markup (TokenizedCode).
                      <span data-shiki-token class={token.className}>{token.content}</span>
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

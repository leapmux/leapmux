import type { JSX } from 'solid-js'
import type { CommandLanguage } from './ir/tools/execute'
import type { MarkdownRenderContext } from './renderContext'
import type { TokenGate } from './useAsyncCodeTokens'
import type { CachedToken } from '~/lib/tokenCache'
import { createMemo, For, Show } from 'solid-js'
import { canHighlightBySize } from './results/collapse'
import { useAsyncCodeTokens } from './useAsyncCodeTokens'

/** Map a markdown render context to the shared token hook's premeasure/hold gate. */
function tokenGateFromContext(context: MarkdownRenderContext | undefined): TokenGate {
  return {
    premeasure: context?.premeasureMode === true,
    hold: context?.syntaxHighlightingPaused?.() === true || context?.textSelectionActive?.() === true,
  }
}

function TokenizedCode(props: {
  code: string
  tokens?: CachedToken[][] | null
  class?: string
  dataCommandInputCollapsed?: boolean
  dataCommandInputOverflowing?: boolean
  elementRef?: (el: HTMLDivElement) => void
}): JSX.Element {
  return (
    <div
      ref={el => props.elementRef?.(el)}
      class={props.class}
      data-command-input-collapsed={props.dataCommandInputCollapsed ? '' : undefined}
      data-command-input-overflowing={props.dataCommandInputOverflowing ? '' : undefined}
    >
      <Show when={props.tokens} fallback={props.code}>
        {lines => (
          <For each={lines()}>
            {(line, index) => (
              <>
                <For each={line}>
                  {token => (
                    <span data-shiki-token class={token.className}>
                      {token.content}
                    </span>
                  )}
                </For>
                <Show when={index() < lines().length - 1}>{'\n'}</Show>
              </>
            )}
          </For>
        )}
      </Show>
    </div>
  )
}

/**
 * A code body highlighted as token <span>s via the async Oniguruma token worker
 * (replacing the old synchronous `codeToHtml` + innerHTML path). While tokens are
 * in flight / paused / oversized, `TokenizedCode` shows the raw text, so there is
 * no flash of nothing. The only per-surface difference is the Shiki `lang`; the
 * eligibility, gate, and token markup are identical, so Bash and JSON are thin
 * wrappers that bind `lang` rather than two copies that must stay in sync.
 */
function AsyncHighlightedCode(props: {
  lang: string
  code: string
  context?: MarkdownRenderContext
  class?: string
  maxHighlightChars?: number
  maxHighlightLines?: number
  dataCommandInputCollapsed?: boolean
  dataCommandInputOverflowing?: boolean
  elementRef?: (el: HTMLDivElement) => void
}): JSX.Element {
  // Memoized so the per-surface char/line scan runs once per code change, not on
  // every currentKey() read inside the hook (2-3x per reactive pass). Mirrors
  // useDiffTokens, which memoizes its eligibility for the same reason.
  const eligible = createMemo(() => canHighlightBySize(props.code, {
    ...(props.maxHighlightChars !== undefined ? { maxChars: props.maxHighlightChars } : {}),
    ...(props.maxHighlightLines !== undefined ? { maxLines: props.maxHighlightLines } : {}),
  }))
  const tokens = useAsyncCodeTokens({
    lang: () => props.lang,
    code: () => props.code,
    eligible,
    gate: () => tokenGateFromContext(props.context),
    rowOffscreen: () => props.context?.rowOffscreen?.() === true,
  })
  return (
    <TokenizedCode
      code={props.code}
      {...(props.class !== undefined ? { class: props.class } : {})}
      {...(props.dataCommandInputCollapsed !== undefined ? { dataCommandInputCollapsed: props.dataCommandInputCollapsed } : {})}
      {...(props.dataCommandInputOverflowing !== undefined ? { dataCommandInputOverflowing: props.dataCommandInputOverflowing } : {})}
      {...(props.elementRef !== undefined ? { elementRef: props.elementRef } : {})}
      tokens={tokens()}
    />
  )
}

export function CommandHighlightHtml(props: {
  code: string
  language?: CommandLanguage
  context?: MarkdownRenderContext
  class?: string
  maxHighlightChars?: number
  maxHighlightLines?: number
  dataCommandInputCollapsed?: boolean
  dataCommandInputOverflowing?: boolean
  elementRef?: (el: HTMLDivElement) => void
}): JSX.Element {
  return (
    <AsyncHighlightedCode
      lang={props.language ?? 'bash'}
      code={props.code}
      {...(props.context !== undefined ? { context: props.context } : {})}
      {...(props.class !== undefined ? { class: props.class } : {})}
      {...(props.maxHighlightChars !== undefined ? { maxHighlightChars: props.maxHighlightChars } : {})}
      {...(props.maxHighlightLines !== undefined ? { maxHighlightLines: props.maxHighlightLines } : {})}
      {...(props.dataCommandInputCollapsed !== undefined ? { dataCommandInputCollapsed: props.dataCommandInputCollapsed } : {})}
      {...(props.dataCommandInputOverflowing !== undefined ? { dataCommandInputOverflowing: props.dataCommandInputOverflowing } : {})}
      {...(props.elementRef !== undefined ? { elementRef: props.elementRef } : {})}
    />
  )
}

export function JsonHighlightHtml(props: {
  code: string
  context?: MarkdownRenderContext
  class?: string
  maxHighlightChars?: number
  maxHighlightLines?: number
}): JSX.Element {
  return (
    <AsyncHighlightedCode
      lang="json"
      code={props.code}
      {...(props.context !== undefined ? { context: props.context } : {})}
      {...(props.class !== undefined ? { class: props.class } : {})}
      {...(props.maxHighlightChars !== undefined ? { maxHighlightChars: props.maxHighlightChars } : {})}
      {...(props.maxHighlightLines !== undefined ? { maxHighlightLines: props.maxHighlightLines } : {})}
    />
  )
}

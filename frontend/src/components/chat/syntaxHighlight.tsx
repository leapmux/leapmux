import type { JSX } from 'solid-js'
import type { CommandLanguage } from './model/tools/execute'
import type { MarkdownRenderContext } from './renderContext'
import type { TokenGate } from './useAsyncCodeTokens'
import type { CachedToken } from '~/lib/tokenCache'
import { createMemo, For, Show } from 'solid-js'
import { canHighlightBySize } from './results/collapse'
import { useAsyncCodeTokens } from './useAsyncCodeTokens'

/** Read premeasure and pause controls for the shared token hook. */
function tokenGateFromContext(context: MarkdownRenderContext | undefined): TokenGate {
  return {
    premeasure: context?.premeasureMode === true,
    hold: context?.syntaxHighlightingPaused?.() === true || context?.textSelectionActive?.() === true,
  }
}

function TokenizedCode(props: {
  dataToolOutputPreview?: boolean
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
      data-tool-output-preview={props.dataToolOutputPreview ? '' : undefined}
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
 * Render code as token spans from the asynchronous token worker.
 * Keep raw text visible while token work waits, pauses, or exceeds its limits.
 * Bash and JSON share token eligibility, controls, and markup.
 * Each wrapper supplies its language.
 */
function AsyncHighlightedCode(props: {
  dataToolOutputPreview?: boolean
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
  // Calculate size eligibility once for each code change.
  // Repeated token-key reads then reuse the same result.
  // useDiffTokens applies this same cache rule.
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
      {...(props.dataToolOutputPreview !== undefined ? { dataToolOutputPreview: props.dataToolOutputPreview } : {})}
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
  dataToolOutputPreview?: boolean
  code: string
  context?: MarkdownRenderContext
  class?: string
  maxHighlightChars?: number
  maxHighlightLines?: number
}): JSX.Element {
  return (
    <AsyncHighlightedCode
      lang="json"
      {...(props.dataToolOutputPreview !== undefined ? { dataToolOutputPreview: props.dataToolOutputPreview } : {})}
      code={props.code}
      {...(props.context !== undefined ? { context: props.context } : {})}
      {...(props.class !== undefined ? { class: props.class } : {})}
      {...(props.maxHighlightChars !== undefined ? { maxHighlightChars: props.maxHighlightChars } : {})}
      {...(props.maxHighlightLines !== undefined ? { maxHighlightLines: props.maxHighlightLines } : {})}
    />
  )
}

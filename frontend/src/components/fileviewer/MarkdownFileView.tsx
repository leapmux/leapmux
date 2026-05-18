import type { JSX } from 'solid-js'
import type { ViewMode } from './ViewToggle'
import type { ParsedCatLine } from '~/components/chat/results/ReadResultView'
import { createMemo, Show } from 'solid-js'
import { markdownContent } from '~/components/chat/markdownEditor/markdownContent.css'
import { ReadResultView } from '~/components/chat/results/ReadResultView'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import { renderMarkdown } from '~/lib/renderMarkdown'
import * as styles from './FileViewer.css'
import { TextFileView } from './TextFileView'

export function MarkdownFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize: number
  /** Controlled view mode (render / source / split). */
  mode: ViewMode
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
}): JSX.Element {
  const text = createMemo(() => new TextDecoder().decode(props.content))
  const html = createMemo(() => renderMarkdown(text()))

  const lines = createMemo((): ParsedCatLine[] => {
    const raw = text()
    if (!raw)
      return []
    const split = raw.split('\n')
    if (split.length > 0 && split.at(-1) === '')
      split.pop()
    return split.map((line, i) => ({ num: i + 1, text: line }))
  })

  // Proportional scroll sync for side-by-side view
  let leftRef!: HTMLDivElement
  let rightRef!: HTMLDivElement
  let rightSourceRef: HTMLDivElement | undefined
  let ignoreScroll = false

  const syncScroll = (source: HTMLDivElement, target: HTMLDivElement) => {
    if (ignoreScroll)
      return
    ignoreScroll = true
    const max = source.scrollHeight - source.clientHeight
    const ratio = max > 0 ? source.scrollTop / max : 0
    target.scrollTop = ratio * (target.scrollHeight - target.clientHeight)
    requestAnimationFrame(() => {
      ignoreScroll = false
    })
  }

  return (
    <div class={styles.toggleViewContainer}>
      <Show when={props.mode === 'render'}>
        <div class={styles.markdownContainer}>
          {/* eslint-disable-next-line solid/no-innerhtml -- intentional: rendered markdown */}
          <div class={markdownContent} innerHTML={html()} />
        </div>
      </Show>
      <Show when={props.mode === 'split'}>
        <div class={styles.splitContainer}>
          <div
            class={styles.splitPane}
            ref={leftRef}
            onScroll={() => syncScroll(leftRef, rightRef)}
          >
            {/* eslint-disable-next-line solid/no-innerhtml -- intentional: rendered markdown */}
            <div class={`${styles.splitPaneMarkdown} ${markdownContent}`} innerHTML={html()} />
          </div>
          <div class={styles.splitDivider} />
          <div
            class={`${styles.splitPane} ${styles.splitPaneSource}`}
            ref={rightRef}
            onScroll={() => syncScroll(rightRef, leftRef)}
          >
            <SelectionQuotePopover
              containerRef={rightSourceRef}
              onQuote={(text, startLine, endLine) => props.onQuote?.(text, startLine, endLine)}
            >
              <div ref={rightSourceRef}>
                <ReadResultView lines={lines()} filePath={props.filePath} />
              </div>
            </SelectionQuotePopover>
          </div>
        </div>
      </Show>
      <Show when={props.mode === 'source'}>
        <TextFileView
          content={props.content}
          filePath={props.filePath}
          totalSize={props.totalSize}
          onQuote={props.onQuote}
        />
      </Show>
    </div>
  )
}

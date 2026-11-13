import type { JSX } from 'solid-js'
import type { NumberedFileLine } from '../chat/model/readFileResult'
import type { FileDisplayMode } from '~/stores/tab.types'
import { createMemo, Show } from 'solid-js'
import { markdownContent } from '~/components/chat/markdownEditor/markdownContent.css'
import { MarkdownText } from '~/components/chat/MarkdownText'
import { ReadResultView } from '~/components/chat/results/ReadResultView'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import * as styles from './FileViewer.css'
import { TextFileView } from './TextFileView'

export function MarkdownFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize: number
  /** Controlled view mode (render / source / split). */
  mode: FileDisplayMode
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
}): JSX.Element {
  const text = createMemo(() => new TextDecoder().decode(props.content))

  const lines = createMemo((): NumberedFileLine[] => {
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
  let ignoreScroll = false

  const syncScroll = (source: HTMLDivElement, target: HTMLDivElement) => {
    if (ignoreScroll)
      return
    ignoreScroll = true
    const max = source.scrollHeight - source.clientHeight
    const ratio = max > 0
      ? source.scrollTop / max
      : 0
    target.scrollTop = ratio * (target.scrollHeight - target.clientHeight)
    requestAnimationFrame(() => {
      ignoreScroll = false
    })
  }

  return (
    <div class={styles.toggleViewContainer}>
      <Show when={props.mode === 'render'}>
        <div class={styles.markdownContainer}>
          <MarkdownText text={text()} />
        </div>
      </Show>
      <Show when={props.mode === 'split'}>
        <div class={styles.splitContainer}>
          <div
            class={styles.splitPane}
            ref={leftRef}
            onScroll={() => syncScroll(leftRef, rightRef)}
          >
            <MarkdownText text={text()} class={`${styles.splitPaneMarkdown} ${markdownContent}`} />
          </div>
          <div class={styles.splitDivider} />
          <div
            class={`${styles.splitPane} ${styles.splitPaneSource}`}
            ref={rightRef}
            onScroll={() => syncScroll(rightRef, leftRef)}
          >
            <SelectionQuotePopover
              onQuote={(text, startLine, endLine) => props.onQuote?.(text, startLine, endLine)}
            >
              <ReadResultView lines={lines()} filePath={props.filePath} />
            </SelectionQuotePopover>
          </div>
        </div>
      </Show>
      <Show when={props.mode === 'source'}>
        <TextFileView
          content={props.content}
          filePath={props.filePath}
          totalSize={props.totalSize}
          {...(props.onQuote !== undefined
            ? { onQuote: props.onQuote }
            : {})}
        />
      </Show>
    </div>
  )
}

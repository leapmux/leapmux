import type { JSX } from 'solid-js'
import type { ParsedCatLine } from '~/components/chat/ReadResultView'
import { createMemo } from 'solid-js'
import { ReadResultView } from '~/components/chat/ReadResultView'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import * as styles from './FileViewer.css'

export function TextFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize: number
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
}): JSX.Element {
  let containerRef: HTMLDivElement | undefined
  const text = createMemo(() => new TextDecoder().decode(props.content))

  const lines = createMemo((): ParsedCatLine[] => {
    const raw = text()
    if (!raw)
      return []
    const split = raw.split('\n')
    // Remove trailing empty line from split
    if (split.length > 0 && split.at(-1) === '')
      split.pop()
    return split.map((line, i) => ({ num: i + 1, text: line }))
  })

  return (
    <div ref={containerRef} class={styles.textViewContainer}>
      <SelectionQuotePopover
        containerRef={containerRef}
        onQuote={(text, startLine, endLine) => props.onQuote?.(text, startLine, endLine)}
      >
        <ReadResultView lines={lines()} filePath={props.filePath} />
      </SelectionQuotePopover>
    </div>
  )
}

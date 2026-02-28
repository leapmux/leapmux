import type { JSX } from 'solid-js'
import type { ParsedCatLine } from '~/components/chat/ReadResultView'
import AtSign from 'lucide-solid/icons/at-sign'
import { createMemo, Show } from 'solid-js'
import { ReadResultView } from '~/components/chat/ReadResultView'
import { Icon } from '~/components/common/Icon'
import { SelectionQuotePopover } from '~/components/common/SelectionQuotePopover'
import * as styles from './FileViewer.css'

export function TextFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize: number
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
  onMention?: () => void
}): JSX.Element {
  let containerRef: HTMLDivElement | undefined
  const text = createMemo(() => new TextDecoder().decode(props.content))

  const lines = createMemo((): ParsedCatLine[] => {
    const raw = text()
    if (!raw)
      return []
    const split = raw.split('\n')
    // Remove trailing empty line from split
    if (split.length > 0 && split[split.length - 1] === '')
      split.pop()
    return split.map((line, i) => ({ num: i + 1, text: line }))
  })

  return (
    <div ref={containerRef} class={styles.textViewContainer} style={{ position: 'relative' }}>
      <Show when={props.onMention}>
        <div class={styles.viewToggle}>
          <button
            class={styles.viewToggleButton}
            onClick={() => props.onMention?.()}
            title="Mention in the chat"
            data-testid="file-mention-button"
          >
            <Icon icon={AtSign} size="sm" />
          </button>
        </div>
      </Show>
      <SelectionQuotePopover
        containerRef={containerRef}
        onQuote={(text, startLine, endLine) => props.onQuote?.(text, startLine, endLine)}
      >
        <ReadResultView lines={lines()} filePath={props.filePath} />
      </SelectionQuotePopover>
    </div>
  )
}

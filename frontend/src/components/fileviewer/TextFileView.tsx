import type { JSX } from 'solid-js'
import type { ParsedCatLine } from '~/components/chat/ReadResultView'
import { createMemo } from 'solid-js'
import { ReadResultView } from '~/components/chat/ReadResultView'
import * as styles from './FileViewer.css'

export function TextFileView(props: {
  content: Uint8Array
  filePath: string
  totalSize: number
}): JSX.Element {
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
    <div class={styles.textViewContainer}>
      <ReadResultView lines={lines()} filePath={props.filePath} />
    </div>
  )
}

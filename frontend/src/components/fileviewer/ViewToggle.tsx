import type { JSX } from 'solid-js'
import Code from 'lucide-solid/icons/code'
import Columns2 from 'lucide-solid/icons/columns-2'
import Eye from 'lucide-solid/icons/eye'
import { Show } from 'solid-js'
import * as styles from './FileViewer.css'

export type ViewMode = 'render' | 'source' | 'split'

export function ViewToggle(props: {
  mode: ViewMode
  onToggle: (mode: ViewMode) => void
  showSplit?: boolean
}): JSX.Element {
  return (
    <div class={styles.viewToggle}>
      <button
        class={styles.viewToggleButton}
        classList={{ [styles.viewToggleActive]: props.mode === 'render' }}
        onClick={() => props.onToggle('render')}
        title="Rendered view"
      >
        <Eye size={14} />
      </button>
      <Show when={props.showSplit}>
        <button
          class={styles.viewToggleButton}
          classList={{ [styles.viewToggleActive]: props.mode === 'split' }}
          onClick={() => props.onToggle('split')}
          title="Side-by-side view"
        >
          <Columns2 size={14} />
        </button>
      </Show>
      <button
        class={styles.viewToggleButton}
        classList={{ [styles.viewToggleActive]: props.mode === 'source' }}
        onClick={() => props.onToggle('source')}
        title="Source view"
      >
        <Code size={14} />
      </button>
    </div>
  )
}

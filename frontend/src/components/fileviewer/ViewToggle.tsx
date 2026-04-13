import type { JSX } from 'solid-js'
import AtSign from 'lucide-solid/icons/at-sign'
import Code from 'lucide-solid/icons/code'
import Columns2 from 'lucide-solid/icons/columns-2'
import Eye from 'lucide-solid/icons/eye'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './FileViewer.css'

export type ViewMode = 'render' | 'source' | 'split'

export function ViewToggle(props: {
  mode: ViewMode
  onToggle: (mode: ViewMode) => void
  showSplit?: boolean
  onMention?: () => void
}): JSX.Element {
  return (
    <div class={styles.viewToggle}>
      <Tooltip text="Rendered view">
        <button
          class={styles.viewToggleButton}
          classList={{ [styles.viewToggleActive]: props.mode === 'render' }}
          onClick={() => props.onToggle('render')}
          aria-label="Rendered view"
        >
          <Icon icon={Eye} size="sm" />
        </button>
      </Tooltip>
      <Show when={props.showSplit}>
        <Tooltip text="Side-by-side view">
          <button
            class={styles.viewToggleButton}
            classList={{ [styles.viewToggleActive]: props.mode === 'split' }}
            onClick={() => props.onToggle('split')}
            aria-label="Side-by-side view"
          >
            <Icon icon={Columns2} size="sm" />
          </button>
        </Tooltip>
      </Show>
      <Tooltip text="Source view">
        <button
          class={styles.viewToggleButton}
          classList={{ [styles.viewToggleActive]: props.mode === 'source' }}
          onClick={() => props.onToggle('source')}
          aria-label="Source view"
        >
          <Icon icon={Code} size="sm" />
        </button>
      </Tooltip>
      <Show when={props.onMention}>
        <div style={{ 'border-left': '1px solid var(--border)' }} />
        <Tooltip text="Mention in the chat">
          <button
            class={styles.viewToggleButton}
            onClick={() => props.onMention?.()}
            aria-label="Mention in the chat"
            data-testid="file-mention-button"
          >
            <Icon icon={AtSign} size="sm" />
          </button>
        </Tooltip>
      </Show>
    </div>
  )
}

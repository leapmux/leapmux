import type { JSX } from 'solid-js'
import Code from 'lucide-solid/icons/code'
import Columns2 from 'lucide-solid/icons/columns-2'
import Eye from 'lucide-solid/icons/eye'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './FileViewer.css'

export type ViewMode = 'render' | 'source' | 'split'

/**
 * Segmented view-mode toggle row used by markdown and SVG-image
 * viewers. Positioning is owned by the parent — this component just
 * paints the button chrome.
 */
export function ViewToggle(props: {
  mode: ViewMode
  onToggle: (mode: ViewMode) => void
  showSplit?: boolean
}): JSX.Element {
  return (
    <div class={styles.viewToggle}>
      <Tooltip text="Rendered view" ariaLabel>
        <button
          class={styles.viewToggleButton}
          classList={{ [styles.viewToggleActive]: props.mode === 'render' }}
          onClick={() => props.onToggle('render')}
        >
          <Icon icon={Eye} size="sm" />
        </button>
      </Tooltip>
      <Show when={props.showSplit}>
        <Tooltip text="Side-by-side view" ariaLabel>
          <button
            class={styles.viewToggleButton}
            classList={{ [styles.viewToggleActive]: props.mode === 'split' }}
            onClick={() => props.onToggle('split')}
          >
            <Icon icon={Columns2} size="sm" />
          </button>
        </Tooltip>
      </Show>
      <Tooltip text="Source view" ariaLabel>
        <button
          class={styles.viewToggleButton}
          classList={{ [styles.viewToggleActive]: props.mode === 'source' }}
          onClick={() => props.onToggle('source')}
        >
          <Icon icon={Code} size="sm" />
        </button>
      </Tooltip>
    </div>
  )
}

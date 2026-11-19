import type { Component } from 'solid-js'
import type { Tab } from '~/stores/tab.types'
import { Tooltip } from '~/components/common/Tooltip'
import { terminalProgressView } from '~/stores/tab.helpers'
import * as styles from './TerminalProgressBar.css'

/**
 * The OSC 9;4 progress of a terminal tab: a thin bar that fills with the
 * percent, beside the tab's title.
 *
 * One component for the tab strip and the sidebar tree, so the two surfaces
 * cannot draw the progress differently. The tooltip states the percent, which
 * the bar alone shows only as a length, and a screen reader reads it as a
 * progressbar. An indeterminate progress has no value, so it states
 * "In progress" and leaves aria-valuenow out.
 */
export const TerminalProgressBar: Component<{ tab: Tab }> = (props) => {
  const view = () => terminalProgressView(props.tab)
  return (
    <Tooltip text={view().label}>
      <span
        class={styles.bar}
        data-testid="tab-progress"
        role="progressbar"
        aria-label="Terminal progress"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={view().indeterminate
          ? undefined
          : view().percent}
        aria-valuetext={view().label}
        style={{ '--progress-percent': `${view().percent}%` }}
      />
    </Tooltip>
  )
}

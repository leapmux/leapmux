import type { Component } from 'solid-js'
import type { TabStatusView } from '~/lib/tabStatus'
import { Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { tabStatusDescription } from '~/lib/tabStatus'
import * as styles from './TabStatusBadge.css'

/**
 * The status that a process reported for a tab (`leapmux control tab status
 * set`), drawn beside the tab's title: a ring that fills with the progress, or
 * a dot when there is no progress, then the text, all in the tone's color.
 *
 * One component for the tab strip and the sidebar tree, so the two surfaces
 * cannot draw a status differently.
 *
 * The text is cut to fit, and the tooltip holds the whole of it with the
 * progress, which the ring alone shows. The tooltip shows ALWAYS rather than
 * when clipped: the percentage is in no visible text, so a status that fits
 * would otherwise hide its number.
 */
export const TabStatusBadge: Component<{ status: TabStatusView }> = props => (
  <Tooltip text={tabStatusDescription(props.status)}>
    <span class={styles.status} data-testid="tab-status" data-tone={props.status.tone}>
      {/* Compared with undefined, not tested for truth: 0 is a progress, and
          it draws an empty ring rather than the dot. */}
      <Show
        when={props.status.progressPercent !== undefined}
        fallback={<span class={styles.dot} aria-hidden="true" />}
      >
        <span
          class={styles.ring}
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={props.status.progressPercent}
          aria-label={props.status.text}
          data-testid="tab-status-progress"
          style={{ '--tab-status-progress': `${props.status.progressPercent}%` }}
        />
      </Show>
      <span class={styles.text} data-testid="tab-status-text">{props.status.text}</span>
    </span>
  </Tooltip>
)

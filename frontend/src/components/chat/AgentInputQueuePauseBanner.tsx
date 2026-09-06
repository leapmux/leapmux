import type { Component } from 'solid-js'
import PauseCircle from 'lucide-solid/icons/circle-pause'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { AgentInputQueuePauseReason } from '~/generated/proto/leapmux/v1/agent_pb'
import { srOnly } from '~/styles/shared.css'
import * as styles from './AgentInputQueuePauseBanner.css'

/**
 * Why the queue stopped, in one sentence for each reason.
 *
 * `satisfies Record<...>` is the enforcement: a sixth pause reason fails the
 * frontend typecheck until somebody writes its sentence here. Four of the five
 * reasons are automatic, so a missing entry would leave a user who pressed
 * nothing with no explanation at all.
 *
 * `UNSPECIFIED` reads as the manual case on purpose. A Worker that sends it has
 * told us nothing, and inventing a cause is worse than describing the state.
 */
const PAUSE_SENTENCES = {
  [AgentInputQueuePauseReason.UNSPECIFIED]: 'Queue paused. New messages wait here until you resume.',
  [AgentInputQueuePauseReason.MANUAL]: 'Queue paused. New messages wait here until you resume.',
  [AgentInputQueuePauseReason.INTERRUPTED]: 'Queue paused because you interrupted the agent.',
  [AgentInputQueuePauseReason.AGENT_STOPPED]: 'Queue paused because the agent stopped.',
  [AgentInputQueuePauseReason.DELIVERY_FAILED]: 'Queue paused because an input did not reach the agent.',
  [AgentInputQueuePauseReason.DELIVERY_UNCERTAIN]: 'Queue paused because an input may not have reached the agent.',
} satisfies Record<AgentInputQueuePauseReason, string>

export function pauseSentence(reason: AgentInputQueuePauseReason): string {
  return PAUSE_SENTENCES[reason] ?? PAUSE_SENTENCES[AgentInputQueuePauseReason.MANUAL]
}

export interface AgentInputQueuePauseBannerProps {
  paused: boolean
  reason: AgentInputQueuePauseReason
  onResume?: () => void
}

/**
 * States that the queue is paused, and why.
 *
 * It reads `paused` alone, never the item count, because the case that needs it
 * most is the empty one: the queue pauses itself, the list renders nothing, and
 * the next message the user sends parks in a queue that will not drain. The
 * pause toggle in the composer's action cluster is not an answer there -- it is
 * icon-only below `sm`, so its whole signal is an icon that swapped.
 *
 * Resume is repeated here rather than pointed at, because the toggle sits in a
 * different row from the sentence a user just read.
 */
export const AgentInputQueuePauseBanner: Component<AgentInputQueuePauseBannerProps> = props => (
  <>
    {/*
      The live region is ALWAYS mounted, and only its text changes. A region
      that appears together with its content announces nothing on several
      screen readers, and four of the five pause reasons arrive with no user
      action to explain them. `srOnly` is absolutely positioned, so it is out
      of flow and adds no flex item and no gap to `inputArea`.
    */}
    <div class={srOnly} role="status" aria-live="polite">
      {props.paused ? pauseSentence(props.reason) : ''}
    </div>
    <Show when={props.paused}>
      <div class={styles.banner} data-testid="queue-pause-banner">
        <Icon icon={PauseCircle} size="xs" class={styles.icon} />
        <span class={styles.text}>{pauseSentence(props.reason)}</span>
        <button
          class={`outline ${styles.resume}`}
          type="button"
          onClick={() => props.onResume?.()}
          data-testid="queue-pause-banner-resume"
        >
          Resume
        </button>
      </div>
    </Show>
  </>
)

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

/**
 * The sentence for a reason, including one this build does not know.
 *
 * A Worker ahead of this client sends an enum number the table has no key for,
 * and protobuf-es passes that number through unchanged. `UNSPECIFIED` is
 * already the "the Worker told us nothing" bucket, so the fallback points
 * there rather than at a second reason that happens to read the same.
 */
export function pauseSentence(reason: AgentInputQueuePauseReason): string {
  return PAUSE_SENTENCES[reason] ?? PAUSE_SENTENCES[AgentInputQueuePauseReason.UNSPECIFIED]
}

export interface AgentInputQueuePauseBannerProps {
  paused: boolean
  reason: AgentInputQueuePauseReason
  /** A resume RPC is in flight, so the button refuses a second press. */
  busy?: boolean
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
 *
 * ONE node carries the sentence, and it is ALWAYS mounted. A live region that
 * appears together with its content announces nothing on several screen
 * readers, and four of the five pause reasons arrive with no user action to
 * explain them -- so the region cannot wait for the pause. A SECOND node
 * holding the same sentence is equally wrong: a screen reader then announces
 * the sentence twice, and every lookup that matches on that text resolves two
 * elements. So the class swaps instead. While the queue runs the region is
 * `srOnly`, which is `position: absolute` and therefore out of `inputArea`'s
 * flex flow, so it opens no gap; once the queue pauses the same node becomes
 * the visible banner.
 */
export const AgentInputQueuePauseBanner: Component<AgentInputQueuePauseBannerProps> = props => (
  <div
    class={props.paused ? styles.banner : srOnly}
    role="status"
    aria-live="polite"
    data-testid={props.paused ? 'queue-pause-banner' : undefined}
  >
    <Show when={props.paused}>
      <Icon icon={PauseCircle} size="xs" class={styles.icon} />
      <span class={styles.text}>{pauseSentence(props.reason)}</span>
      <button
        class={`outline ${styles.resume}`}
        type="button"
        disabled={props.busy}
        onClick={() => props.onResume?.()}
        data-testid="queue-pause-banner-resume"
      >
        Resume
      </button>
    </Show>
  </div>
)

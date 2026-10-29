import type { Component } from 'solid-js'
import MessageCircleQuestion from 'lucide-solid/icons/message-circle-question'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { srOnly } from '~/styles/shared.css'
import * as styles from './PendingQuestionsBanner.css'

export interface PendingQuestionsBannerProps {
  /** How many questions wait. */
  count: number
  /** Whether to show the banner right now. */
  shown: boolean
  onAnswer: () => void
}

/**
 * States that the agent asked a question, and how many wait.
 *
 * The composer stays free while a question waits, so this one line is the only
 * sign that anything is pending until the reader opens it.
 *
 * ONE node carries the sentence, and it is ALWAYS mounted, for the same reason
 * as `AgentInputQueuePauseBanner`: a live region that appears together with its
 * content announces nothing on several screen readers, and a second node with
 * the same text would make every text lookup resolve two elements. So the class
 * swaps instead, and `data-testid` exists only while the banner shows -- the
 * hidden node is 1px, which Playwright treats as visible.
 */
export const PendingQuestionsBanner: Component<PendingQuestionsBannerProps> = props => (
  <div
    class={props.shown ? styles.banner : srOnly}
    role="status"
    aria-live="polite"
    data-testid={props.shown ? 'pending-questions-banner' : undefined}
  >
    <Show when={props.shown}>
      <Icon icon={MessageCircleQuestion} size="xs" class={styles.icon} />
      <span class={styles.text}>
        {props.count === 1
          ? 'The agent asked a question.'
          : `The agent asked ${props.count} questions.`}
      </span>
      <button
        class={`outline ${styles.answer}`}
        type="button"
        onClick={() => props.onAnswer()}
        data-testid="pending-questions-answer"
      >
        Answer
      </button>
    </Show>
  </div>
)

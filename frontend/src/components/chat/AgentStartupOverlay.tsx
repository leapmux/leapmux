import type { Component } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import * as styles from './ChatView.css'

/**
 * Rendered in place of the "Send a message to start" empty state while
 * the agent subprocess is still starting up (AgentStatus.STARTING).
 *
 * The editor beneath remains mounted and focusable so the user can
 * compose ahead; the pending-message queue in TileRenderer defers the
 * actual RPC until the agent transitions to ACTIVE.
 */
export const AgentStartupOverlay: Component<{ providerLabel: string }> = (props) => {
  return (
    <div
      class={styles.emptyChat}
      data-testid="agent-startup-overlay"
      style={{ 'display': 'inline-flex', 'align-items': 'center', 'gap': '0.5em' }}
    >
      <Icon icon={LoaderCircle} size="sm" class={spinner} />
      <span>
        Starting
        {' '}
        {props.providerLabel}
        …
      </span>
    </div>
  )
}

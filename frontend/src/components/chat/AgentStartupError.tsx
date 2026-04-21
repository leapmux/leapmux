import type { Component } from 'solid-js'
import * as styles from './ChatView.css'

/**
 * Rendered when the agent subprocess failed to start
 * (AgentStatus.STARTUP_FAILED). Shows the formatted error (phase +
 * stderr + shell preamble) in --danger, plus a "Close tab" button so
 * the user can dismiss a dead tab without hunting for the x.
 */
export const AgentStartupError: Component<{
  providerLabel: string
  error: string
  onCloseTab?: () => void
}> = (props) => {
  return (
    <div
      class={styles.emptyChat}
      data-testid="agent-startup-error"
      style={{
        'color': 'var(--danger)',
        'white-space': 'pre-wrap',
        'text-align': 'left',
      }}
    >
      <strong>
        {props.providerLabel}
        {' '}
        failed to start
      </strong>
      <br />
      {props.error || 'Unknown error'}
      {props.onCloseTab && (
        <div style={{ 'margin-top': '12px' }}>
          <button type="button" onClick={props.onCloseTab} data-testid="agent-startup-error-close">
            Close tab
          </button>
        </div>
      )}
    </div>
  )
}

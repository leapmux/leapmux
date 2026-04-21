import type { Component } from 'solid-js'
import * as styles from './TerminalView.css'

/**
 * Rendered when the PTY failed to start (TerminalStatus.STARTUP_FAILED).
 * Shows the server-formatted error in --danger with a Close-tab button.
 */
export const TerminalStartupError: Component<{
  active: boolean
  error: string
  onCloseTab?: () => void
}> = (props) => {
  return (
    <div
      class={styles.terminalWrapper}
      data-testid="terminal-startup-error"
      style={{
        'display': props.active ? 'flex' : 'none',
        'flex-direction': 'column',
        'align-items': 'center',
        'justify-content': 'center',
        'white-space': 'pre-wrap',
        'text-align': 'center',
        'padding': '24px',
        'color': 'var(--danger)',
      }}
    >
      <strong>Terminal failed to start</strong>
      <br />
      {props.error || 'Unknown error'}
      {props.onCloseTab && (
        <div style={{ 'margin-top': '12px' }}>
          <button type="button" onClick={props.onCloseTab} data-testid="terminal-startup-error-close">
            Close tab
          </button>
        </div>
      )}
    </div>
  )
}

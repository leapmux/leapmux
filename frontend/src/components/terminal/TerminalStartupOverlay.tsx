import type { Component } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import * as styles from './TerminalView.css'

/**
 * Rendered in place of the xterm canvas while the PTY is still
 * starting. The xterm container is not mounted yet — that happens once
 * the terminal transitions to 'running' — so input is naturally gated.
 */
export const TerminalStartupOverlay: Component<{ active: boolean }> = (props) => {
  return (
    <div
      class={styles.terminalWrapper}
      data-testid="terminal-startup-overlay"
      style={{
        'display': props.active ? 'flex' : 'none',
        'flex-direction': 'column',
        'align-items': 'center',
        'justify-content': 'center',
        'padding': '24px',
      }}
    >
      <div style={{ 'display': 'inline-flex', 'align-items': 'center', 'gap': '0.5em' }}>
        <Icon icon={LoaderCircle} size="sm" class={spinner} />
        <span>Starting terminal…</span>
      </div>
    </div>
  )
}

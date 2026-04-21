import type { Component, JSX } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import * as styles from './StartupPanel.css'

/**
 * Spinner icon + label row used while an agent or terminal subprocess is
 * starting. Layout-agnostic: the caller owns the wrapper (chat
 * empty-state, absolute overlay over xterm, etc.) and just drops this in.
 */
export const StartupSpinner: Component<{ label: JSX.Element }> = props => (
  <div style={{ 'display': 'inline-flex', 'align-items': 'center', 'gap': '0.5em' }}>
    <Icon icon={LoaderCircle} size="sm" class={spinner} />
    <span>{props.label}</span>
  </div>
)

/**
 * Body shown when a subprocess startup has failed terminally: a
 * heading with the failure summary, followed by the server-formatted
 * error details rendered as a soft-wrapped monospace code block.
 * Layout-agnostic — the caller owns the outer wrapper (and its danger
 * color, which these children inherit).
 */
export const StartupErrorBody: Component<{
  title: JSX.Element
  error: string
}> = props => (
  <div class={styles.startupErrorBody}>
    <h2 class={styles.startupErrorTitle}>{props.title}</h2>
    <pre class={styles.startupErrorDetails}>
      <code>{props.error || 'Unknown error'}</code>
    </pre>
  </div>
)

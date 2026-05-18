import type { Component, JSX } from 'solid-js'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import * as styles from './StartupPanel.css'

/**
 * Spinner icon + label row used while an agent or terminal subprocess is
 * starting. Layout-agnostic: the caller owns the wrapper (chat
 * empty-state, absolute overlay over xterm, etc.) and just drops this in.
 */
export const StartupSpinner: Component<{ label: JSX.Element }> = props => (
  <div class={styles.startupSpinner}>
    <Icon icon={LoaderCircle} size="sm" class={spinner} />
    <span>{props.label}</span>
  </div>
)

/**
 * Generic title + body + actions column used as the shared layout for
 * informational fallbacks (e.g. the file-viewer "binary file" view) and
 * the failure body below. Caller decides text color — the children
 * inherit `color` from the wrapper.
 */
export const StartupBody: Component<{
  title: JSX.Element
  body?: JSX.Element
  /** Optional id on the `<h2>` so callers can wire `aria-labelledby`. */
  titleId?: string
  children?: JSX.Element
}> = props => (
  <div class={styles.startupBody}>
    <h2 class={styles.startupTitle} id={props.titleId}>{props.title}</h2>
    {props.body}
    <Show when={props.children}>
      <div class={styles.startupActions}>{props.children}</div>
    </Show>
  </div>
)

/**
 * Body shown when a subprocess startup has failed terminally: a
 * heading with the failure summary, followed by the server-formatted
 * error details rendered as a soft-wrapped monospace code block.
 * Layered on `StartupBody` for visual consistency.
 */
export const StartupErrorBody: Component<{
  title: JSX.Element
  error: string
}> = props => (
  <StartupBody
    title={props.title}
    body={(
      <pre class={styles.startupErrorDetails}>
        <code>{props.error || 'Unknown error'}</code>
      </pre>
    )}
  />
)

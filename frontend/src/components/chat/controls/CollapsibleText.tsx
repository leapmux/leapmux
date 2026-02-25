import type { JSX } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import * as styles from '../ControlRequestBanner.css'

interface CollapsibleTextProps {
  text: string
  /** Maximum number of lines to show before collapsing. */
  maxLines: number
  /** Tag to wrap the text in. Default: 'pre' */
  tag?: 'pre' | 'div'
  class?: string
}

export function CollapsibleText(props: CollapsibleTextProps): JSX.Element {
  const [expanded, setExpanded] = createSignal(false)

  const lines = () => props.text.split('\n')
  const shouldCollapse = () => lines().length > props.maxLines
  const visibleText = () =>
    shouldCollapse() && !expanded()
      ? lines().slice(0, props.maxLines).join('\n')
      : props.text
  const hiddenCount = () => lines().length - props.maxLines

  return (
    <>
      <Dynamic component={props.tag ?? 'pre'} class={props.class}>{visibleText()}</Dynamic>
      <Show when={shouldCollapse()}>
        <button
          class={styles.collapsibleToggle}
          onClick={() => setExpanded(prev => !prev)}
        >
          {expanded()
            ? 'Show less'
            : `Show ${hiddenCount()} more line${hiddenCount() === 1 ? '' : 's'}\u2026`}
        </button>
      </Show>
    </>
  )
}

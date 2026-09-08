import type { JSX } from 'solid-js'
import { createSignal, createUniqueId, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { CollapsibleToggle } from '~/components/common/CollapsibleToggle'
import { pluralize } from '~/lib/plural'

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
  const bodyId = createUniqueId()

  const lines = () => props.text.split('\n')
  const shouldCollapse = () => lines().length > props.maxLines
  const visibleText = () =>
    shouldCollapse() && !expanded()
      ? lines().slice(0, props.maxLines).join('\n')
      : props.text
  const hiddenCount = () => lines().length - props.maxLines

  return (
    <>
      <Dynamic component={props.tag ?? 'pre'} class={props.class} id={bodyId}>{visibleText()}</Dynamic>
      <Show when={shouldCollapse()}>
        <CollapsibleToggle
          expanded={expanded()}
          onToggle={() => setExpanded(prev => !prev)}
          controls={bodyId}
          moreLabel={`Show ${pluralize(hiddenCount(), 'more line')}\u2026`}
        />
      </Show>
    </>
  )
}

import type { JSX } from 'solid-js'
import { createSignal, For, Show } from 'solid-js'
import { CollapsibleToggle } from '~/components/common/CollapsibleToggle'

interface CollapsibleListProps<T> {
  items: T[]
  maxVisible: number
  renderItem: (item: T) => JSX.Element
  /** Label for the "show more" toggle. Receives the count of hidden items. Default: "Show N more..." */
  moreLabel?: (hiddenCount: number) => string
  /** Label for the "show less" toggle. Default: "Show less" */
  lessLabel?: string
}

export function CollapsibleList<T>(props: CollapsibleListProps<T>): JSX.Element {
  const [expanded, setExpanded] = createSignal(false)

  const shouldCollapse = () => props.items.length > props.maxVisible
  const visibleItems = () =>
    shouldCollapse() && !expanded()
      ? props.items.slice(0, props.maxVisible)
      : props.items
  const hiddenCount = () => props.items.length - props.maxVisible

  return (
    <>
      <For each={visibleItems()}>
        {item => props.renderItem(item)}
      </For>
      {/* No `controls`: the items are siblings with no wrapper to point at.
          `aria-expanded` still states the open state, which is the half that
          matters most here. */}
      <Show when={shouldCollapse()}>
        <CollapsibleToggle
          expanded={expanded()}
          onToggle={() => setExpanded(prev => !prev)}
          moreLabel={props.moreLabel?.(hiddenCount()) ?? `Show ${hiddenCount()} more\u2026`}
          lessLabel={props.lessLabel}
        />
      </Show>
    </>
  )
}

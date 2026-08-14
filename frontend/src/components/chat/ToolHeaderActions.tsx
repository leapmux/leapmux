import type { JSX } from 'solid-js'
import type { MessageAction, MessageActionId, ToolHeaderActionsCallerProps, ToolHeaderActionsLayoutProps } from './messageActions'
import { createMemo, For, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { buildMessageActions, leadingActions, trailingActions } from './messageActions'
import { RelativeTime } from './RelativeTime'
import { toolHeaderActions, toolHeaderTimestamp } from './toolStyles.css'

/**
 * Actions area for a message row or a tool header: timestamp, the copies, Quote,
 * then the diff and expand toggles -- all with tooltips.
 *
 * The buttons come from `buildMessageActions` rather than being written out here,
 * so the row's context menu (~/components/chat/MessageContextMenuHost.tsx) offers
 * exactly the same set. Only the ORDER is this component's own.
 *
 * The cells render through `<For>` keyed on the action id. `buildMessageActions`
 * returns fresh objects whenever any input changes -- a copy flipping to its
 * "Copied" state is such a change -- and `<For>` reuses the mapped element while
 * the id survives, so the strip updates in place: the icon and title computations
 * re-run, the `<button>` and its focus stay, and an id that leaves the list
 * unmounts its cell instead of lingering as a stale read.
 */
export function ToolHeaderActions(props: {
  caller?: ToolHeaderActionsCallerProps
  layout?: ToolHeaderActionsLayoutProps
}): JSX.Element {
  const layout = () => props.layout
  const actions = createMemo(() => buildMessageActions(props.caller, props.layout))

  /** The cells the leading group renders: the timestamp slot plus the action ids. */
  type LeadingCell = 'timestamp' | MessageActionId

  /**
   * The action for `id` as it stands now. Called only for ids the lists below
   * still carry: `<For>` unmounts a removed action's cell, so no computation
   * outlives its action.
   */
  const liveAction = (id: MessageActionId): MessageAction =>
    actions().find(a => a.id === id)!

  const timestampEl = (
    <Show when={layout()?.createdAt}>
      <RelativeTime
        timestamp={layout()!.createdAt!}
        class={toolHeaderTimestamp}
      />
    </Show>
  )

  const cell = (id: LeadingCell): JSX.Element =>
    id === 'timestamp'
      ? timestampEl
      : (
          <IconButton
            icon={liveAction(id).icon}
            size="sm"
            data-testid={liveAction(id).testId}
            onClick={(e: MouseEvent) => {
              const action = liveAction(id)
              if (action.stopPropagation)
                e.stopPropagation()
              action.run()
            }}
            title={liveAction(id).label}
          />
        )

  /*
    One order, READ LEFT TO RIGHT ON SCREEN, for every row: timestamp, then the
    copies from broadest (the whole envelope) to narrowest (the rendered content),
    then Quote. A tool header and a message row used to disagree here, which put
    the same two buttons in different places depending on the row above.

    A MIRRORED row (a right-aligned user message) moves Quote only, from last to
    second, so it still lands nearest the bubble on its right. Everything else
    keeps its place: timestamp, Quote, JSON, Markdown, Content. `leadingActions`
    owns both orders.

    A mirrored row's SOURCE order is not its render order. That toolbar is a
    two-column `direction: rtl` grid (see `messageRowEnd > toolHeaderActions` in
    ~/components/chat/messageStyles.css.ts), so the FIRST item of each pair lands
    in the RIGHT cell:

      source [Quote, timestamp]  renders  timestamp  Quote
      source [Markdown, JSON]    renders  JSON       Markdown
      source [Content]           renders             Content

    `swapPairs` below derives that source order from the screen order, so the two
    arms cannot drift apart the way two hand-written lists could. `<For>` then
    reorders the SAME elements into that order, keeping each button's node (and
    focus) through the flip.
  */
  const leadingCells = createMemo((): LeadingCell[] => {
    const mirrored = layout()?.mirrored === true
    // `timestampEl` holds its cell whether or not it renders anything: it is one
    // item of the mirrored grid's pairing, and dropping it for a message with no
    // `createdAt` would re-pair every button after it.
    const screen: LeadingCell[] = [
      'timestamp',
      ...leadingActions(actions(), mirrored).map(action => action.id),
    ]
    return mirrored ? swapPairs(screen) : screen
  })

  const trailingCells = createMemo(() => trailingActions(actions()).map(action => action.id))

  return (
    <div class={toolHeaderActions} data-testid="message-toolbar">
      <For each={leadingCells()}>{cell}</For>
      <For each={trailingCells()}>{cell}</For>
    </div>
  )
}

/**
 * Swap each adjacent pair, leaving a trailing odd item in place. Turns a
 * left-to-right screen order into the source order a two-column `direction: rtl`
 * grid needs, because that grid puts the first item of each pair in the right cell.
 */
function swapPairs<T>(items: T[]): T[] {
  const out = items.slice()
  for (let i = 0; i + 1 < out.length; i += 2)
    [out[i], out[i + 1]] = [out[i + 1], out[i]]
  return out
}

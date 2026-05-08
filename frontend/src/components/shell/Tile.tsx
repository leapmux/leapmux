import type { Component, JSX } from 'solid-js'
import type { TileActions, TilePopAction } from './TileActionsMenu'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import LayoutGrid from 'lucide-solid/icons/layout-grid'
import PictureInPicture2 from 'lucide-solid/icons/picture-in-picture-2'
import X from 'lucide-solid/icons/x'
import { For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { useTileSize } from '~/hooks/useTileSize'
import { shortcutHint } from '~/lib/shortcuts/display'
import { closeAffordance } from '~/stores/layout.store'
import { useGridPopover } from './GridPopoverHost'
import * as styles from './Tile.css'
import { SPLIT_ACTIONS, TileActionsMenu } from './TileActionsMenu'

interface TileProps {
  tileId: string
  isFocused: boolean
  /** Tile-level actions (split, grid, close) and their predicates. */
  actions: TileActions
  tabBar: JSX.Element
  children: JSX.Element
  onFocus: () => void
  /** Pop-out / pop-in affordance; absent when the tile can't pop. */
  pop?: TilePopAction
}

// Wrap an IconButton click handler so the click doesn't bubble up to the
// tile's onClick={onFocus}. The thunk lets `props.actions.*` reads stay
// reactive — capturing the bound function reference here would break
// reactivity if the actions object ever swapped.
function stopAnd(fn: () => void) {
  return (e: MouseEvent) => {
    e.stopPropagation()
    fn()
  }
}

export const Tile: Component<TileProps> = (props) => {
  let tileRef: HTMLDivElement | undefined
  let mainGridButtonRef: HTMLElement | undefined
  let tinyGridAnchorRef: HTMLElement | undefined

  const { sizeClass, heightClass } = useTileSize(() => tileRef)
  // Single shared popover lives at the shell root — see
  // `GridPopoverHostProvider`. Tests must wrap their render in the same
  // provider; missing context surfaces below at request time as a clear
  // error rather than a silent no-op.
  const popoverHost = useGridPopover()

  const close = () => closeAffordance(props.actions.closeMode, 'button')

  // The splitActions strip has its own border + padding, so an empty
  // strip leaves a visible bordered gap next to the tab bar. Skip
  // rendering it entirely when none of its buttons would show.
  const hasSplitActions = () =>
    !!props.pop
    || props.actions.canSplit
    || props.actions.canMakeGrid
    || props.actions.closeMode.kind !== 'none'

  const requestOpenGridPopover = (anchor: HTMLElement | undefined) => {
    if (!popoverHost)
      throw new Error('GridPopoverHostProvider missing — wrap Tile renders (incl. tests) in <GridPopoverHostProvider>.')
    if (!anchor)
      return
    popoverHost.open({ anchor, onSelect: props.actions.onMakeGrid })
  }

  return (
    <div
      ref={tileRef}
      class={styles.tile}
      classList={{ [styles.tileFocused]: props.isFocused }}
      onClick={() => props.onFocus()}
      data-testid="tile"
      data-tile-id={props.tileId}
      data-tile-size={sizeClass()}
      data-tile-height={heightClass()}
    >
      <div class={styles.tabBarRow}>
        <div class={styles.tabBarFiller}>
          {props.tabBar}
        </div>
        <Show when={hasSplitActions()}>
          <div class={styles.splitActions}>
            <Show when={props.pop}>
              {pop => (
                <IconButton
                  icon={PictureInPicture2}
                  size="md"
                  onClick={stopAnd(() => pop().onClick())}
                  data-testid={pop().testId}
                  title={shortcutHint(pop().label, 'app.toggleFloatingTab')}
                />
              )}
            </Show>
            <Show when={props.actions.canSplit}>
              <For each={SPLIT_ACTIONS}>
                {action => (
                  <IconButton
                    icon={action.icon}
                    size="md"
                    onClick={stopAnd(() => props.actions.onSplit(action.direction))}
                    data-testid={action.testId}
                    title={shortcutHint(action.label, action.shortcutId)}
                  />
                )}
              </For>
            </Show>
            <Show when={props.actions.canMakeGrid}>
              <IconButton
                ref={(el) => { mainGridButtonRef = el }}
                icon={LayoutGrid}
                size="md"
                onClick={stopAnd(() => requestOpenGridPopover(mainGridButtonRef))}
                data-testid="make-grid"
                title="Make grid"
              />
            </Show>
            <Show when={props.actions.closeMode.kind !== 'none'}>
              <IconButton
                icon={X}
                size="md"
                onClick={stopAnd(() => props.actions.onClose())}
                data-testid={close().testId}
                title={close().label}
              />
            </Show>
          </div>
        </Show>
      </div>
      {/* Floating overlay trigger for tiny height tiles */}
      <Show when={heightClass() === 'tiny'}>
        <DropdownMenu
          trigger={triggerProps => (
            <IconButton
              icon={Ellipsis}
              size="md"
              class={styles.tinyOverlayTrigger}
              title="Tile menu"
              {...triggerProps}
              ref={(el) => {
                tinyGridAnchorRef = el
                triggerProps.ref(el)
              }}
              onClick={(e: MouseEvent) => {
                e.stopPropagation()
                triggerProps.onClick()
              }}
            />
          )}
        >
          <TileActionsMenu
            actions={props.actions}
            // The DropdownMenu auto-closes on click; defer the popover open
            // so the menu unmount completes first and the size popover
            // anchors against the persistent overflow trigger.
            // eslint-disable-next-line solid/reactivity -- queueMicrotask runs the callback synchronously off the event handler; we don't depend on signal tracking inside it.
            onMakeGridClick={() => queueMicrotask(() => requestOpenGridPopover(tinyGridAnchorRef))}
            makeGridLabel="Make a grid…"
            pop={props.pop}
          />
        </DropdownMenu>
      </Show>
      <div class={styles.tileContent}>
        {props.children}
      </div>
    </div>
  )
}

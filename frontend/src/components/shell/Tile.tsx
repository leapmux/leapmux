import type { Component, JSX } from 'solid-js'
import Columns2 from 'lucide-solid/icons/columns-2'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import Rows2 from 'lucide-solid/icons/rows-2'
import X from 'lucide-solid/icons/x'
import { Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { useTileSize } from '~/hooks/useTileSize'
import * as styles from './Tile.css'

interface TileProps {
  tileId: string
  isFocused: boolean
  canClose: boolean
  canSplit: boolean
  tabBar: JSX.Element
  children: JSX.Element
  onFocus: () => void
  onSplitHorizontal: () => void
  onSplitVertical: () => void
  onClose: () => void
}

export const Tile: Component<TileProps> = (props) => {
  let tileRef: HTMLDivElement | undefined

  const { sizeClass, heightClass } = useTileSize(() => tileRef)

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
        <div class={styles.splitActions}>
          <Show when={props.canSplit}>
            <IconButton
              icon={Columns2}
              size="md"
              onClick={(e) => {
                e.stopPropagation()
                props.onSplitHorizontal()
              }}
              data-testid="split-horizontal"
              title="Split vertical"
            />
            <IconButton
              icon={Rows2}
              size="md"
              onClick={(e) => {
                e.stopPropagation()
                props.onSplitVertical()
              }}
              data-testid="split-vertical"
              title="Split horizontal"
            />
          </Show>
          <Show when={props.canClose}>
            <IconButton
              icon={X}
              size="md"
              onClick={(e) => {
                e.stopPropagation()
                props.onClose()
              }}
              data-testid="close-tile"
              title="Close tile"
            />
          </Show>
        </div>
      </div>
      {/* Floating overlay trigger for tiny height tiles */}
      <Show when={heightClass() === 'tiny'}>
        <DropdownMenu
          trigger={triggerProps => (
            <IconButton
              icon={Ellipsis}
              size="md"
              class={styles.tinyOverlayTrigger}
              onClick={e => e.stopPropagation()}
              title="Tile menu"
              {...triggerProps}
            />
          )}
        >
          <Show when={props.canSplit}>
            <button
              role="menuitem"
              onClick={() => props.onSplitHorizontal()}
            >
              Split vertical
            </button>
            <button
              role="menuitem"
              onClick={() => props.onSplitVertical()}
            >
              Split horizontal
            </button>
          </Show>
          <Show when={props.canClose}>
            <button
              role="menuitem"
              onClick={() => props.onClose()}
            >
              Close tile
            </button>
          </Show>
        </DropdownMenu>
      </Show>
      <div class={styles.tileContent}>
        {props.children}
      </div>
    </div>
  )
}

import type { Component, JSX } from 'solid-js'
import Columns2 from 'lucide-solid/icons/columns-2'
import Ellipsis from 'lucide-solid/icons/ellipsis'
import PictureInPicture2 from 'lucide-solid/icons/picture-in-picture-2'
import Rows2 from 'lucide-solid/icons/rows-2'
import X from 'lucide-solid/icons/x'
import { Show } from 'solid-js'
import { DropdownMenu, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { useTileSize } from '~/hooks/useTileSize'
import { getShortcutHint, shortcutHint } from '~/lib/shortcuts/display'
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
  onPopOut?: () => void
  onPopIn?: () => void
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
          <Show when={props.onPopOut ?? props.onPopIn}>
            {(handler) => {
              const isPopOut = () => !!props.onPopOut
              return (
                <IconButton
                  icon={PictureInPicture2}
                  size="md"
                  onClick={(e) => {
                    e.stopPropagation()
                    handler()()
                  }}
                  data-testid={isPopOut() ? 'pop-out-button' : 'pop-in-button'}
                  title={shortcutHint(
                    isPopOut() ? 'Pop out to floating window' : 'Pop in to main window',
                    'app.toggleFloatingTab',
                  )}
                />
              )
            }}
          </Show>
          <Show when={props.canSplit}>
            <IconButton
              icon={Columns2}
              size="md"
              onClick={(e) => {
                e.stopPropagation()
                props.onSplitHorizontal()
              }}
              data-testid="split-horizontal"
              title={shortcutHint('Split vertical', 'app.splitTileHorizontal')}
            />
            <IconButton
              icon={Rows2}
              size="md"
              onClick={(e) => {
                e.stopPropagation()
                props.onSplitVertical()
              }}
              data-testid="split-vertical"
              title={shortcutHint('Split horizontal', 'app.splitTileVertical')}
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
              title="Tile menu"
              {...triggerProps}
              onClick={(e: MouseEvent) => {
                e.stopPropagation()
                triggerProps.onClick()
              }}
            />
          )}
        >
          <Show when={props.onPopOut ?? props.onPopIn}>
            {handler => (
              <button
                role="menuitem"
                onClick={() => handler()()}
              >
                <DropdownMenuItemContent
                  label={props.onPopOut ? 'Pop out to floating window' : 'Pop in to main window'}
                  shortcut={getShortcutHint('app.toggleFloatingTab')}
                />
              </button>
            )}
          </Show>
          <Show when={props.canSplit}>
            <button
              role="menuitem"
              onClick={() => props.onSplitHorizontal()}
            >
              <DropdownMenuItemContent
                label="Split vertical"
                shortcut={getShortcutHint('app.splitTileHorizontal')}
              />
            </button>
            <button
              role="menuitem"
              onClick={() => props.onSplitVertical()}
            >
              <DropdownMenuItemContent
                label="Split horizontal"
                shortcut={getShortcutHint('app.splitTileVertical')}
              />
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

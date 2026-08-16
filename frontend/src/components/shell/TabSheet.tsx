import type { Component, JSX } from 'solid-js'
import { createDroppable } from '@thisbeyond/solid-dnd'
import { createEffect, createSignal, onCleanup, Show } from 'solid-js'
import { motion } from '~/styles/tokens'
import * as styles from './TabBar.css'
import { TABBAR_ZONE_PREFIX } from './TabDragContext'

/**
 * The sheet's list. A component of its own so the zone droppable's
 * registration follows the list's mount: a closed sheet holds no droppable
 * whose clipped-invisible layout could win a collision during an unrelated
 * drag.
 */
const TabSheetList: Component<{ tileId: string, children: JSX.Element }> = (props) => {
  // Zone droppable for cross-tile drops. May fail if the DragDropProvider
  // context is not available.
  let zoneDroppable: ReturnType<typeof createDroppable> | undefined
  try {
    // eslint-disable-next-line solid/reactivity -- stable identifier for createDroppable
    zoneDroppable = createDroppable(`${TABBAR_ZONE_PREFIX}${props.tileId}`)
  }
  catch { /* DragDropProvider context not available */ }
  return (
    <div
      class={styles.sheetList}
      role="tablist"
      ref={(el) => {
        zoneDroppable?.(el)
      }}
      data-testid="tab-sheet-list"
    >
      {props.children}
    </div>
  )
}

interface TabSheetProps {
  /** Whether the sheet is open — the shell's overlay state owns this. */
  open: () => boolean
  onClose: () => void
  /** The tile whose zone the list registers as a drop target. */
  tileId: string
  /** How many tabs the header counts. */
  tabCount: () => number
  /** The list rows, built by the tab bar that owns the row renderers. */
  children: JSX.Element
}

/**
 * The mobile tab sheet: a panel dropping from directly under the tab bar,
 * inside a clip window that keeps the slide from ever crossing the bar
 * itself. (The scrim is MobileLayout's: it anchors to the content region
 * below the bar, so the bar stays bright and its chip toggles the sheet.)
 *
 * The panel is always mounted (class-flipped, like the mobile drawers, so
 * the drop-down transition runs). The LIST is not: rows mount when the sheet
 * opens and unmount after the slide-out, so a closed sheet holds no
 * invisible droppables, no keyboard-focusable rows, and no per-row drag
 * state for the whole session.
 */
export const TabSheet: Component<TabSheetProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  let unmountTimer: ReturnType<typeof setTimeout> | undefined

  const [listMounted, setListMounted] = createSignal(false)
  createEffect(() => {
    if (props.open()) {
      if (unmountTimer) {
        clearTimeout(unmountTimer)
        unmountTimer = undefined
      }
      setListMounted(true)
    }
    else if (listMounted()) {
      // Keep the rows through the slide-out, then unmount.
      unmountTimer = setTimeout(() => {
        unmountTimer = undefined
        setListMounted(false)
      }, motion.medium + 50)
    }
  })
  onCleanup(() => {
    if (unmountTimer)
      clearTimeout(unmountTimer)
  })

  // Focus the panel when it opens so its Escape handler has a target. The
  // panel is always mounted (class-flipped for the slide), so the focus call
  // is what makes the keyboard path work at all.
  createEffect(() => {
    if (props.open())
      requestAnimationFrame(() => panelRef?.focus({ preventScroll: true }))
  })

  return (
    <>
      <div class={styles.sheetPanelClip}>
        <div
          ref={(el) => {
            panelRef = el
          }}
          role="dialog"
          aria-modal="true"
          aria-label="Switch tab"
          // Hidden from assistive tech once the list is gone: the closed
          // panel is an empty shell sliding above the bar.
          aria-hidden={listMounted() ? undefined : 'true'}
          class={styles.sheetPanel}
          classList={{ [styles.sheetPanelOpen]: props.open() }}
          tabIndex={-1}
          data-testid="tab-sheet"
          onKeyDown={(e) => {
            if (e.key === 'Escape')
              props.onClose()
          }}
        >
          <div class={styles.sheetHeader}>
            <span class={styles.sheetTitle} data-testid="tab-sheet-title">
              {props.tabCount()}
              {' '}
              Tab
              {props.tabCount() === 1 ? '' : 's'}
            </span>
          </div>
          <Show when={listMounted()}>
            <TabSheetList tileId={props.tileId}>
              {props.children}
            </TabSheetList>
          </Show>
        </div>
      </div>
    </>
  )
}

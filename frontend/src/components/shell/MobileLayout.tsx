import type { Component, JSX } from 'solid-js'
import type { SwipeDirection } from '~/lib/horizontalSwipe'
import { createSignal, onCleanup } from 'solid-js'
import { assertNever } from '~/lib/assertNever'
import { attachHorizontalSwipe } from '~/lib/horizontalSwipe'
import * as styles from './AppShell.css'

/**
 * Which mobile overlay is up. One state owns the exclusion between the two
 * drawers and the tab sheet: a value other than `'none'` IS the open overlay,
 * so two overlays can never be up at once by construction.
 */
export type MobileOverlay = 'none' | 'left' | 'right' | 'sheet'

/**
 * The overlay a horizontal swipe leaves up.
 *
 * One rule, from either side: **a swipe moves the screen the way the finger
 * goes**. A finger travelling right drags the left edge inward, so it pulls the
 * left drawer in from a clear screen and pushes the right drawer back out. A
 * finger travelling left does the mirror of that.
 *
 * That is why there is no separate "close" direction to state. The swipe that
 * closes a drawer is the same swipe that would have opened the OTHER one, and
 * the open drawer takes it first — which is also what stops one gesture from
 * closing one drawer and opening its opposite in a single flick.
 *
 * Total, and it returns the CURRENT overlay for a swipe that does nothing, so
 * the caller has one assignment and no null case. Setting a signal to the value
 * it already holds is a no-op.
 */
export function nextOverlayForSwipe(current: MobileOverlay, direction: SwipeDirection): MobileOverlay {
  switch (current) {
    case 'none':
      return direction === 'right' ? 'left' : 'right'
    case 'left':
      return direction === 'left' ? 'none' : current
    case 'right':
      return direction === 'right' ? 'none' : current
    case 'sheet':
      // The sheet drops from the tab bar and closes through the chip that
      // opened it. It covers the swipe region entirely, so a finger under it is
      // not reaching for a drawer.
      return current
    default:
      return assertNever(current)
  }
}

/**
 * The one owner of the mobile overlay state. Every toggle path — the bar's
 * drawer buttons, the chip, the keyboard shortcuts, workspace selection, a
 * swipe across the content region — routes through these mutators, which is
 * what keeps the exclusion structural instead of a close-the-other call at each
 * site.
 */
export function createMobileOverlayState() {
  const [overlay, setOverlay] = createSignal<MobileOverlay>('none')
  return {
    overlay,
    toggleDrawer: (side: 'left' | 'right') =>
      setOverlay(prev => (prev === side ? 'none' : side)),
    toggleSheet: () =>
      setOverlay(prev => (prev === 'sheet' ? 'none' : 'sheet')),
    closeSheet: () =>
      setOverlay(prev => (prev === 'sheet' ? 'none' : prev)),
    closeAll: () => setOverlay('none'),
    applySwipe: (direction: SwipeDirection) =>
      setOverlay(prev => nextOverlayForSwipe(prev, direction)),
  }
}

interface MobileLayoutProps {
  leftSidebarOpen: boolean
  rightSidebarOpen: boolean
  /** Whether the tab sheet is open (the scrim renders from this). */
  sheetOpen: boolean
  onCloseSheet: () => void
  leftSidebarElement: JSX.Element
  rightSidebarElement: JSX.Element
  tabBarElement: JSX.Element
  /**
   * Whether to drop the bar from the layout entirely. Owned by the caller,
   * which is the one place that knows both that the soft keyboard is up and
   * that no overlay depends on the bar's toggles.
   */
  tabBarHidden: boolean
  tileContent: JSX.Element
  editorPanel: JSX.Element | false
  /**
   * Act on a horizontal swipe across the content region. Wired to the overlay
   * owner's `applySwipe`; see `nextOverlayForSwipe` for what each swipe means.
   */
  onSwipe: (direction: SwipeDirection) => void
}

/**
 * The mobile shell: the tab bar in normal flow, and one content region below
 * it holding everything else. The region's top edge IS the bar's bottom edge,
 * so the drawers and the sheet scrim anchor to it absolutely — flush by
 * construction, with no measured bar height or safe-area arithmetic anywhere.
 * The region owns a stacking context below the bar, so the bar stays bright
 * and tappable over an open drawer, and the sheet panel that drops from the
 * bar covers both.
 */
export const MobileLayout: Component<MobileLayoutProps> = (props) => {
  /**
   * Arm the swipe on the content region — the one band that holds the tiles AND
   * both drawers, so the same gesture opens a drawer over the tiles and closes
   * the drawer that covers them. The drawers are full-bleed and leave no strip
   * of scrim to tap, which makes the swipe their only pointer-driven dismissal.
   *
   * NOT on the shell: the tab bar sits above this band and carries the drawer
   * toggles, the tab chip and its own strip. A swipe there belongs to the bar.
   */
  function armSwipe(el: HTMLElement) {
    onCleanup(attachHorizontalSwipe(el, { onSwipe: direction => props.onSwipe(direction) }))
  }

  return (
    <div class={styles.mobileShell}>
      <div
        class={styles.mobileTabBar}
        classList={{ [styles.mobileTabBarHidden]: props.tabBarHidden }}
      >
        {props.tabBarElement}
      </div>

      <div class={styles.mobileCenter} ref={armSwipe}>
        <div class={styles.mobileTilePaneSlot}>
          {props.tileContent}
        </div>
        {props.editorPanel}

        {/* Both panels stay mounted and slide by transform, so an E2E spec
            cannot ask whether they are "visible" — a closed drawer is, off to
            the side. `toBeInViewport` against these two is the oracle. */}
        <div
          class={styles.mobileSidebar}
          classList={{ [styles.mobileSidebarOpen]: props.leftSidebarOpen }}
          data-testid="mobile-drawer-left"
        >
          {props.leftSidebarElement}
        </div>

        <div
          class={`${styles.mobileSidebar} ${styles.mobileSidebarRight}`}
          classList={{ [styles.mobileSidebarOpen]: props.rightSidebarOpen }}
          data-testid="mobile-drawer-right"
        >
          {props.rightSidebarElement}
        </div>

        <div
          class={styles.sheetOverlay}
          classList={{ [styles.sheetOverlayOpen]: props.sheetOpen }}
          onClick={() => props.onCloseSheet()}
          aria-hidden="true"
          data-testid="tab-sheet-overlay"
        />
      </div>
    </div>
  )
}

import type { Component, JSX } from 'solid-js'
import { createSignal } from 'solid-js'
import * as styles from './AppShell.css'

/**
 * Which mobile overlay is up. One state owns the exclusion between the two
 * drawers and the tab sheet: a value other than `'none'` IS the open overlay,
 * so two overlays can never be up at once by construction.
 */
export type MobileOverlay = 'none' | 'left' | 'right' | 'sheet'

/**
 * The one owner of the mobile overlay state. Every toggle path — the bar's
 * drawer buttons, the chip, the keyboard shortcuts, workspace selection —
 * routes through these mutators, which is what keeps the exclusion
 * structural instead of a close-the-other call at each site.
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
  return (
    <div class={styles.mobileShell}>
      <div
        class={styles.mobileTabBar}
        classList={{ [styles.mobileTabBarHidden]: props.tabBarHidden }}
      >
        {props.tabBarElement}
      </div>

      <div class={styles.mobileCenter}>
        <div class={styles.mobileTilePaneSlot}>
          {props.tileContent}
        </div>
        {props.editorPanel}

        <div
          class={styles.mobileSidebar}
          classList={{ [styles.mobileSidebarOpen]: props.leftSidebarOpen }}
        >
          {props.leftSidebarElement}
        </div>

        <div
          class={`${styles.mobileSidebar} ${styles.mobileSidebarRight}`}
          classList={{ [styles.mobileSidebarOpen]: props.rightSidebarOpen }}
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

import type { Component } from 'solid-js'
import MenuIcon from 'lucide-solid/icons/menu'
import PanelLeft from 'lucide-solid/icons/panel-left'
import PanelRight from 'lucide-solid/icons/panel-right'
import { createSignal, onCleanup, Show } from 'solid-js'
import { observeWindowMaximized, openWebInspector, quitApp, windowClose, windowMinimize, windowToggleMaximize } from '~/api/platformBridge'
import { DropdownMenu, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { getShortcutHint, getShortcutHintsText, shortcutHint } from '~/lib/shortcuts/display'
import { getPlatform } from '~/lib/shortcuts/platform'
import { isDesktopApp } from '~/lib/systemInfo'
import { menuSectionHeader } from '~/styles/shared.css'
import { headerHeightPx } from '~/styles/tokens'
import * as styles from './CustomTitlebar.css'
import { PanelLeftFilled, PanelRightFilled } from './SidebarIcons'
import { UserMenuItems } from './UserMenuItems'
import { WindowCloseIcon, WindowMaximizeIcon, WindowMinimizeIcon, WindowRestoreIcon } from './WindowControlIcons'

const platform = getPlatform()
const desktop = isDesktopApp()
const isLinuxDesktop = desktop && platform === 'linux'
const isWindowsDesktop = desktop && platform === 'windows'
const MAC_TRAFFIC_LIGHT_INSET = '78px'
const WINDOWS_CAPTION_BUTTON_INSET = '138px'
const macPadding = desktop && platform === 'mac' ? MAC_TRAFFIC_LIGHT_INSET : undefined
const windowsPadding = desktop && platform === 'windows' ? WINDOWS_CAPTION_BUTTON_INSET : undefined
const hamburgerPlacement = platform === 'mac'
  ? { placement: 'auto' as const, xOffset: 78, yOffset: headerHeightPx }
  : { placement: 'auto' as const }

interface CustomTitlebarProps {
  onToggleLeftSidebar: () => void
  onToggleRightSidebar: () => void
  leftSidebarVisible: boolean
  rightSidebarVisible: boolean
}

export const CustomTitlebar: Component<CustomTitlebarProps> = (props) => {
  const [isMaximized, setIsMaximized] = createSignal(false)
  onCleanup(observeWindowMaximized(setIsMaximized))

  const maximizeLabel = () => (isMaximized() ? 'Restore' : 'Maximize')

  return (
    <div
      class={styles.titlebar}
      style={{
        'padding-left': macPadding,
        'padding-right': windowsPadding,
      }}
    >
      <DropdownMenu
        trigger={(
          <IconButton
            icon={MenuIcon}
            iconSize="lg"
            size="md"
            class={styles.menuTrigger}
            title="Menu"
            data-testid="app-menu-trigger"
          />
        )}
        placement={hamburgerPlacement}
        data-testid="app-menu"
      >
        <UserMenuItems aboutLabel="About LeapMux Desktop..." />
        <Show when={desktop}>
          <hr />
          <li class={menuSectionHeader}>Window</li>
          <button role="menuitem" onClick={() => void windowMinimize()}>
            <DropdownMenuItemContent label="Minimize" />
          </button>
          <button role="menuitem" onClick={() => void windowToggleMaximize()}>
            <DropdownMenuItemContent label={maximizeLabel()} />
          </button>
          <Show when={desktop}>
            <button role="menuitem" onClick={() => openWebInspector()}>
              <DropdownMenuItemContent label="Open Web Inspector" shortcut={getShortcutHintsText('app.openWebInspector')} />
            </button>
            <button role="menuitem" onClick={() => quitApp()}>
              <DropdownMenuItemContent label="Quit" shortcut={getShortcutHint('app.quit')} />
            </button>
          </Show>
        </Show>
      </DropdownMenu>
      <div class={styles.dragRegion} data-tauri-drag-region />
      <div class={styles.titleText}>LeapMux Desktop</div>

      <IconButton
        icon={props.leftSidebarVisible ? PanelLeftFilled : PanelLeft}
        iconSize="lg"
        size="md"
        title={shortcutHint('Toggle left sidebar', 'app.toggleLeftSidebar')}
        onClick={() => props.onToggleLeftSidebar()}
      />
      <IconButton
        icon={props.rightSidebarVisible ? PanelRightFilled : PanelRight}
        iconSize="lg"
        size="md"
        title={shortcutHint('Toggle right sidebar', 'app.toggleRightSidebar')}
        onClick={() => props.onToggleRightSidebar()}
      />

      <Show when={isLinuxDesktop}>
        <div class={styles.windowControls}>
          <IconButton
            icon={WindowMinimizeIcon}
            iconSize="lg"
            size="md"
            title="Minimize"
            onClick={() => void windowMinimize()}
          />
          <IconButton
            icon={isMaximized() ? WindowRestoreIcon : WindowMaximizeIcon}
            iconSize="lg"
            size="md"
            title={maximizeLabel()}
            onClick={() => void windowToggleMaximize()}
          />
          <IconButton
            icon={WindowCloseIcon}
            iconSize="lg"
            size="md"
            title="Close"
            onClick={() => void windowClose()}
          />
        </div>
      </Show>
    </div>
  )
}

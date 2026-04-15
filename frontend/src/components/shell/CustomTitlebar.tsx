import type { Component } from 'solid-js'
import MenuIcon from 'lucide-solid/icons/menu'
import PanelLeft from 'lucide-solid/icons/panel-left'
import PanelRight from 'lucide-solid/icons/panel-right'
import { createSignal, onCleanup, Show } from 'solid-js'
import { observeWindowMaximized, openWebInspector, quitApp, windowClose, windowMinimize, windowToggleMaximize } from '~/api/platformBridge'
import { DropdownMenu, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { setShowAboutDialog } from '~/components/shell/UserMenu'
import { getShortcutHint, shortcutHint } from '~/lib/shortcuts/display'
import { getPlatform } from '~/lib/shortcuts/platform'
import { isDesktopApp } from '~/lib/systemInfo'
import { menuSectionHeader } from '~/styles/shared.css'
import * as styles from './CustomTitlebar.css'
import { PanelLeftFilled, PanelRightFilled } from './SidebarIcons'
import { WindowCloseIcon, WindowMaximizeIcon, WindowMinimizeIcon, WindowRestoreIcon } from './WindowControlIcons'

const platform = getPlatform()
const desktop = isDesktopApp()
const isLinuxDesktop = desktop && platform === 'linux'
const isWindowsDesktop = desktop && platform === 'windows'
const showHamburgerMenu = isLinuxDesktop || isWindowsDesktop
const MAC_TRAFFIC_LIGHT_INSET = '78px'
const WINDOWS_CAPTION_BUTTON_INSET = '138px'
const macPadding = desktop && platform === 'mac' ? MAC_TRAFFIC_LIGHT_INSET : undefined
const windowsPadding = desktop && platform === 'windows' ? WINDOWS_CAPTION_BUTTON_INSET : undefined

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
      <Show when={showHamburgerMenu}>
        <DropdownMenu
          trigger={(
            <IconButton
              icon={MenuIcon}
              iconSize="lg"
              size="md"
              title="Menu"
              data-testid="app-menu-trigger"
            />
          )}
          data-testid="app-menu"
        >
          <li class={menuSectionHeader}>File</li>
          <button role="menuitem" onClick={() => quitApp()}>
            <DropdownMenuItemContent label="Quit" shortcut={getShortcutHint('app.quit')} />
          </button>
          <hr />
          <li class={menuSectionHeader}>Window</li>
          <button role="menuitem" onClick={() => void windowMinimize()}>
            <DropdownMenuItemContent label="Minimize" />
          </button>
          <button role="menuitem" onClick={() => void windowToggleMaximize()}>
            <DropdownMenuItemContent label={maximizeLabel()} />
          </button>
          <hr />
          <li class={menuSectionHeader}>Help</li>
          <button role="menuitem" onClick={() => setShowAboutDialog(true)}>
            <DropdownMenuItemContent label="About LeapMux Desktop..." />
          </button>
          <button role="menuitem" onClick={() => openWebInspector()}>
            <DropdownMenuItemContent label="Open Web Inspector" shortcut={getShortcutHint('app.openWebInspector')} />
          </button>
        </DropdownMenu>
      </Show>
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

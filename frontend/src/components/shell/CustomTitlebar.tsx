import type { Component } from 'solid-js'
import type { WindowMode } from '~/api/platformBridge'
import MenuIcon from 'lucide-solid/icons/menu'
import PanelLeft from 'lucide-solid/icons/panel-left'
import PanelRight from 'lucide-solid/icons/panel-right'
import { createSignal, onCleanup, Show } from 'solid-js'
import { observeWindowMode, openWebInspector, quitApp, windowClose, windowExitFullscreen, windowMinimize, windowToggleMaximize } from '~/api/platformBridge'
import { DropdownMenu, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import { getShortcutHintsText, shortcutHint } from '~/lib/shortcuts/display'
import { getPlatform } from '~/lib/shortcuts/platform'
import { isDesktopApp } from '~/lib/systemInfo'
import * as styles from './CustomTitlebar.css'
import { OpenInEditorButton } from './OpenInEditorButton'
import { PanelLeftFilled, PanelRightFilled } from './SidebarIcons'
import { AppAboutMenuItem, UserMenuItems } from './UserMenuItems'
import { WindowCloseIcon, WindowMaximizeIcon, WindowMinimizeIcon, WindowRestoreIcon } from './WindowControlIcons'

const platform = getPlatform()
const desktop = isDesktopApp()
// Linux and Windows run with `decorations: false`, so the app renders its own
// min/max/close buttons. macOS keeps native traffic lights.
const showCustomWindowControls = desktop && (platform === 'linux' || platform === 'windows')
const isMacDesktop = desktop && platform === 'mac'
const MAC_TRAFFIC_LIGHT_INSET_PX = 78

interface WorkspaceCustomTitlebarProps {
  variant?: 'workspace'
  onToggleLeftSidebar: () => void
  onToggleRightSidebar: () => void
  leftSidebarVisible: boolean
  rightSidebarVisible: boolean
  /** Working directory of the active tab, or undefined when nothing is active. */
  activeWorkingDir?: () => string | undefined
}

interface MinimalCustomTitlebarProps {
  variant: 'minimal'
}

type CustomTitlebarProps = WorkspaceCustomTitlebarProps | MinimalCustomTitlebarProps

const WorkspaceActions: Component<WorkspaceCustomTitlebarProps> = props => (
  <>
    <OpenInEditorButton workingDir={() => props.activeWorkingDir?.()} />

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
  </>
)

export const CustomTitlebar: Component<CustomTitlebarProps> = (props) => {
  const [windowMode, setWindowMode] = createSignal<WindowMode>('normal')
  onCleanup(observeWindowMode(setWindowMode))

  const isMaximized = () => windowMode() === 'maximized'
  const isFullscreen = () => windowMode() === 'fullscreen'
  // In fullscreen the maximize control becomes the only in-titlebar way out, so
  // it relabels and routes to setFullscreen(false) instead of toggleMaximize
  // (which can't leave fullscreen).
  const maximizeLabel = () => (isFullscreen() ? 'Exit Full Screen' : isMaximized() ? 'Restore' : 'Maximize')
  const onMaximizeControl = () => (isFullscreen() ? void windowExitFullscreen() : void windowToggleMaximize())
  // Reserve space for the macOS native traffic lights, except in fullscreen
  // where they are hidden — otherwise the menu is stranded past an empty gap.
  const macPadding = () => (isMacDesktop && !isFullscreen() ? `${MAC_TRAFFIC_LIGHT_INSET_PX}px` : undefined)

  return (
    <div
      class={styles.titlebar}
      style={{
        'padding-left': macPadding(),
      }}
    >
      <DropdownMenu
        trigger={triggerProps => (
          <IconButton
            icon={MenuIcon}
            iconSize="lg"
            size="md"
            class={styles.menuTrigger}
            title="Menu"
            data-testid="app-menu-trigger"
            {...triggerProps}
          />
        )}
        data-testid="app-menu"
      >
        <Show when={props.variant !== 'minimal'} fallback={<AppAboutMenuItem />}>
          <UserMenuItems />
        </Show>
        <Show when={desktop}>
          <hr />
          <button role="menuitem" onClick={() => void windowMinimize()}>
            <DropdownMenuItemContent label="Minimize" />
          </button>
          <button role="menuitem" onClick={onMaximizeControl}>
            <DropdownMenuItemContent label={maximizeLabel()} />
          </button>
          <button role="menuitem" onClick={() => openWebInspector()}>
            <DropdownMenuItemContent label="Open Web Inspector" shortcut={getShortcutHintsText('app.openWebInspector')} />
          </button>
          <button role="menuitem" onClick={() => quitApp()}>
            <DropdownMenuItemContent label="Quit" shortcut={getShortcutHintsText('app.quit')} />
          </button>
        </Show>
      </DropdownMenu>
      <div class={styles.dragRegion} data-tauri-drag-region />
      <div class={styles.titleText}>LeapMux Desktop</div>

      <Show when={props.variant !== 'minimal'}>
        <WorkspaceActions {...(props as WorkspaceCustomTitlebarProps)} />
      </Show>

      <Show when={showCustomWindowControls}>
        <div class={styles.windowControls}>
          <IconButton
            icon={WindowMinimizeIcon}
            iconSize="lg"
            size="md"
            title="Minimize"
            onClick={() => void windowMinimize()}
          />
          <IconButton
            icon={isMaximized() || isFullscreen() ? WindowRestoreIcon : WindowMaximizeIcon}
            iconSize="lg"
            size="md"
            title={maximizeLabel()}
            onClick={onMaximizeControl}
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

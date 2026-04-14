import type { Component } from 'solid-js'
import CircleX from 'lucide-solid/icons/circle-x'
import Maximize2 from 'lucide-solid/icons/maximize-2'
import Minimize2 from 'lucide-solid/icons/minimize-2'
import PanelLeft from 'lucide-solid/icons/panel-left'
import PanelRight from 'lucide-solid/icons/panel-right'
import { Show } from 'solid-js'
import { windowClose, windowMinimize, windowToggleMaximize } from '~/api/platformBridge'
import { IconButton } from '~/components/common/IconButton'
import { shortcutHint } from '~/lib/shortcuts/display'
import { getPlatform } from '~/lib/shortcuts/platform'
import { isDesktopApp } from '~/lib/systemInfo'
import * as styles from './CustomTitlebar.css'
import { PanelLeftFilled, PanelRightFilled } from './SidebarIcons'

const platform = getPlatform()
const desktop = isDesktopApp()
const isLinuxDesktop = desktop && platform === 'linux'
const macPadding = desktop && platform === 'mac' ? '78px' : undefined
const windowsPadding = desktop && platform === 'windows' ? '138px' : undefined

interface CustomTitlebarProps {
  onToggleLeftSidebar: () => void
  onToggleRightSidebar: () => void
  leftSidebarVisible: boolean
  rightSidebarVisible: boolean
}

export const CustomTitlebar: Component<CustomTitlebarProps> = (props) => {
  return (
    <div
      class={styles.titlebar}
      style={{
        'padding-left': macPadding,
        'padding-right': windowsPadding,
      }}
    >
      <div class={styles.dragRegion} data-tauri-drag-region />

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
            icon={Minimize2}
            iconSize="lg"
            size="md"
            title="Minimize"
            onClick={() => void windowMinimize()}
          />
          <IconButton
            icon={Maximize2}
            iconSize="lg"
            size="md"
            title="Maximize"
            onClick={() => void windowToggleMaximize()}
          />
          <IconButton
            icon={CircleX}
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

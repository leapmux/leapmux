import type { ParentComponent } from 'solid-js'
import { useLocation } from '@solidjs/router'
import { Show } from 'solid-js'
import { isTauriApp } from '~/api/platformBridge'
import { hasWorkspaceDesktopChrome } from '~/lib/desktopChrome'
import { CustomTitlebar } from './CustomTitlebar'
import * as styles from './CustomTitlebar.css'

export const DesktopMinimalChrome: ParentComponent = props => (
  <div class={styles.titlebarLayout}>
    <CustomTitlebar variant="minimal" />
    <div class={styles.minimalTitlebarContent}>
      {props.children}
    </div>
  </div>
)

export const DesktopRouteChrome: ParentComponent = (props) => {
  const location = useLocation()
  const shouldRenderMinimalChrome = () => isTauriApp() && !hasWorkspaceDesktopChrome(location.pathname)

  return (
    <Show
      when={shouldRenderMinimalChrome()}
      fallback={props.children}
    >
      <DesktopMinimalChrome>
        {props.children}
      </DesktopMinimalChrome>
    </Show>
  )
}

import type { Component } from 'solid-js'
import Plus from 'lucide-solid/icons/plus'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { StartupErrorBody } from '~/components/common/StartupPanel'
import { ThemeChooser } from '~/components/common/ThemeChooser'
import { useThemeChooser } from '~/components/common/useThemeChooser'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import { themeStore } from '~/lib/themeStore'
import * as styles from './AppShell.css'

interface WorkspaceCenterFallbackProps {
  /**
   * True when no workspace is selected at all. The caller decides what
   * "none" means from its own signals (an empty id AND an empty list
   * lookup) so this component stays layout-agnostic — desktop and mobile
   * render the same ladder from the same inputs.
   */
  noWorkspace: boolean
  /** The watchdog fired and the bootstrap still has not delivered a workspace. */
  bootstrapTimedOut: boolean
  onNewWorkspace: () => void
}

/**
 * What the center panel shows when there is no workspace to tile: the
 * no-workspace empty state (a create-workspace affordance), or — for a
 * workspace whose state never arrived — the bootstrap watchdog's report.
 * With neither flag set nothing renders: silence is only right while the
 * workspace can still arrive.
 *
 * Extracted from DesktopLayout's fallback arm so the mobile shell can
 * render the identical states instead of a blank panel.
 */
export const WorkspaceCenterFallback: Component<WorkspaceCenterFallbackProps> = (props) => {
  const themeBinding = useThemeChooser()
  return (
    <Show
      when={props.noWorkspace}
      fallback={(
        <Show when={props.bootstrapTimedOut}>
          <div class={styles.emptyTileActions} data-testid="workspace-bootstrap-failed">
            <StartupErrorBody
              title="Couldn't load this workspace"
              error="The workspace's state never arrived. Check your connection to the hub, then reload."
            />
            <button class="outline" onClick={() => window.location.reload()}>
              <span class={styles.emptyTileActionContent}><span>Reload</span></span>
            </button>
          </div>
        </Show>
      )}
    >
      <div class={styles.emptyTileActions} data-testid="no-workspace-empty-state">
        <button
          class="outline"
          data-testid="create-workspace-button"
          onClick={() => props.onNewWorkspace()}
        >
          <Icon icon={Plus} size="sm" />
          <span class={styles.emptyTileActionContent}>
            <span>Create a new workspace...</span>
            <Show when={getShortcutHintsText('app.newWorkspaceDialog')}>
              {shortcut => <span class={styles.emptyTileActionShortcut}>{shortcut()}</span>}
            </Show>
          </span>
        </button>
        {/*
          A user with no workspace yet is looking at their first screen of the
          app, so the theme is offered here as well as in Preferences. Not
          restricted to solo mode: this fallback is mode-agnostic by design, and a
          hub or web user's first impression is the same screen.
        */}
        <ThemeChooser value={themeBinding.value()} onChange={themeBinding.onChange} align="center" systemMode={themeStore.systemMode()} />
      </div>
    </Show>
  )
}

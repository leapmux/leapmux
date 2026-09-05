import type { Component } from 'solid-js'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createResource, createSignal, Show } from 'solid-js'
import { getRuntimeState } from '~/api/platformBridge'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { ExternalAppIcon } from '~/components/common/ExternalAppIcons'
import { ExternalAppMenuItems } from '~/components/common/ExternalAppMenuItems'
import { Tooltip } from '~/components/common/Tooltip'
import { useExternalApps } from '~/hooks/useExternalApps'
import { shortcutHint } from '~/lib/shortcuts/display'
import { spinner } from '~/styles/animations.css'
import * as styles from './OpenInAppButton.css'

interface OpenInAppButtonProps {
  /** Active tab's working directory, reactively read. */
  workingDir: () => string | undefined
}

export const OpenInAppButton: Component<OpenInAppButtonProps> = (props) => {
  // Solo-Desktop gate: ask the Rust shell, not the URL. In `task dev-desktop`
  // the webview points at http://localhost:4328, so a URL-based check would
  // wrongly classify a solo run as non-solo. The runtime state's `localSolo`
  // capability flag reflects the actual sidecar shell mode.
  //
  // `localSolo` ALONE, deliberately -- not `~/lib/workerLocality`'s
  // `isLocalWorker`, which additionally requires the worker to be the bundled
  // auto-registered one. This component takes a `workingDir` and no
  // `workerId`, so it has no worker to ask about; the directory it opens is
  // whatever the active tab reports. A surface that DOES hold a worker id (the
  // sidebar's repository blocks) must use `isLocalWorker`, because a solo
  // desktop can also hold registrations for remote machines.
  // Caught for the same reason AppShell catches it: Solid re-throws a rejected
  // resource from the accessor, and `platformBridge` caches the rejected
  // promise, so one failed IPC call would take out every render that reads it.
  const [runtimeState] = createResource(async () =>
    getRuntimeState().catch(() => undefined),
  )
  const eligible = () => runtimeState()?.capabilities.localSolo ?? false

  const external = useExternalApps(eligible)
  const [menuOpen, setMenuOpen] = createSignal(false)

  const launch = (id: string) => {
    const dir = props.workingDir()
    if (dir)
      external.launch(id, dir)
  }

  const handleMainClick = () => {
    const app = external.preferred()
    if (app) {
      launch(app.id)
    }
    else {
      // No usable remembered application → open the menu so the user can pick.
      setMenuOpen(true)
    }
  }

  const mainTooltip = () => {
    const app = external.preferred()
    const label = app ? `Open in ${app.displayName}` : 'Open in external application'
    return shortcutHint(label, 'app.openInExternalApp')
  }

  // Stay rendered while refreshing so the chevron's spinner can run even if
  // the freshly-fetched application list is momentarily empty.
  const visible = () =>
    eligible() && !!props.workingDir() && (external.apps().length > 0 || external.refreshing())

  // Position the popover relative to the whole split-button container, not
  // the chevron alone. Anchoring to the chevron (which is small and lives
  // near the right edge of the title bar) makes the menu's auto-flip clamp
  // it leftward into a column that overlaps the "Open in …" face. Anchoring
  // to the container keeps the menu's left edge aligned with the button.
  let containerRef: HTMLDivElement | undefined

  return (
    <Show when={visible()}>
      <div
        ref={el => (containerRef = el)}
        class={styles.splitButton}
        data-testid="open-in-app"
      >
        <Tooltip text={mainTooltip()} ariaLabel>
          <button
            type="button"
            class={styles.mainFace}
            onClick={handleMainClick}
            data-testid="open-in-app-main"
          >
            <Show
              when={external.preferred()}
              fallback={(
                <>
                  <ExternalAppIcon size={14} />
                  <span>Open in …</span>
                </>
              )}
            >
              {app => (
                <>
                  <ExternalAppIcon id={app().id} size={14} />
                  <span>
                    Open in
                    {' '}
                    {app().displayName}
                  </span>
                </>
              )}
            </Show>
          </button>
        </Tooltip>
        <DropdownMenu
          class={styles.menu}
          data-testid="open-in-app-menu"
          open={menuOpen}
          onToggle={setMenuOpen}
          anchorRef={() => containerRef}
          trigger={triggerProps => (
            <Tooltip text={external.refreshing() ? 'Refreshing application list…' : 'Choose application'} ariaLabel>
              <button
                type="button"
                // Intentionally NOT calling triggerProps.ref — that would
                // make the chevron the positioning anchor. We want the whole
                // splitButton container (passed via anchorRef above) to be
                // the anchor, so the menu's left edge aligns with the
                // button's left edge instead of being clamped leftward by
                // auto-flip near the viewport's right edge.
                onPointerDown={triggerProps.onPointerDown}
                onClick={triggerProps.onClick}
                aria-expanded={triggerProps['aria-expanded']}
                aria-haspopup="menu"
                class={styles.chevronFace}
                disabled={external.refreshing()}
                data-testid="open-in-app-chevron"
              >
                <Show when={external.refreshing()} fallback={<ChevronDown size={12} />}>
                  <RefreshCw size={12} class={spinner} />
                </Show>
              </button>
            </Tooltip>
          )}
        >
          {/* Picking a row LAUNCHES it and remembers it, the same as every
              other surface that renders this list. It used to only remember,
              which left the menu looking like the launcher it is not. */}
          <ExternalAppMenuItems
            apps={external.apps}
            preferredId={external.preferredId}
            onSelect={launch}
            onRefresh={() => void external.refresh()}
            refreshing={external.refreshing}
            testIdPrefix="open-in-app"
          />
        </DropdownMenu>
      </div>
    </Show>
  )
}

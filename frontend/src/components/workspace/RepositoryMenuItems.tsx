import type { Component } from 'solid-js'
import type { ExternalApps } from '~/hooks/useExternalApps'
import { Show } from 'solid-js'
import { revealInFileManager } from '~/api/platformBridge'
import { ExternalAppMenuItems } from '~/components/common/ExternalAppMenuItems'
import { SubMenu } from '~/components/common/SubMenu'
import { copyTextToClipboard } from '~/lib/clipboard'
import { isFileManager } from '~/lib/externalApps'
import { menuSectionHeader } from '~/styles/shared.css'
import { menuItem } from './workspaceMenuItem'

/** One checkout, with the two facts its actions depend on. */
export interface RepositoryCheckout {
  /** Working-tree root. What every action here acts on. */
  gitToplevel: string
  /** Origin URL, or empty for a repository with no remote. */
  originUrl: string
  /** Whether the worker holding this checkout is THIS machine. */
  isLocal: boolean
}

export interface RepositoryMenuItemsProps {
  checkout: () => RepositoryCheckout
  apps: ExternalApps
  /**
   * Prefix for the `Open in ...` submenu's test ids. One menu of these mounts
   * per row and per checkout, so a shared id would address whichever copy the
   * DOM holds first.
   */
  testIdPrefix: string
}

/**
 * The `Repository` section: everything a user can do to one checkout without
 * changing it.
 *
 * Four surfaces render exactly this block -- the workspace row menu, the
 * branch row menu, the repository row menu, and each per-checkout submenu the
 * last two open -- so a user who learns it once has learned all four. It was
 * one surface with three items before, and the other three had none.
 *
 * Every action is a read, which is why an ARCHIVED workspace keeps the whole
 * block: copying a URL, copying a path, revealing a directory and opening an
 * application all leave the workspace exactly as it was.
 */
export const RepositoryMenuItems: Component<RepositoryMenuItemsProps> = (props) => {
  const toplevel = () => props.checkout().gitToplevel

  return (
    <>
      <li class={menuSectionHeader}>Repository</li>

      {/* Hidden with no origin: there is no URL to copy. */}
      <Show when={props.checkout().originUrl}>
        {url => menuItem('Copy repository URL', () => void copyTextToClipboard(url()))}
      </Show>

      {/* NOT gated on locality, unlike the three below. A remote worker's
          path is exactly the thing a user wants on the clipboard -- to paste
          into an ssh session on the machine that has it. */}
      {menuItem('Copy repository path', () => void copyTextToClipboard(toplevel()))}

      {/* These open the LOCAL file manager or the LOCAL application, so a
          remote worker's absolute path either does not exist here or --
          worse -- exists and is a different directory. */}
      <Show when={props.checkout().isLocal}>
        {menuItem('Reveal in file manager', () => void revealInFileManager(toplevel()))}

        {/* Dropped when the remembered application IS the file manager: the
            row would read "Open in Finder" directly under "Reveal in file
            manager" and say almost the same thing. The submenu below still
            offers it, so nothing becomes unreachable. */}
        <Show when={!isFileManager(props.apps.preferred()) ? props.apps.preferred() : undefined}>
          {app => menuItem(`Open in ${app().displayName}`, () => props.apps.launch(app().id, toplevel()))}
        </Show>

        <Show when={props.apps.apps().length > 0}>
          <SubMenu
            label="Open in…"
            data-testid={`${props.testIdPrefix}-open-in`}
            popoverTestId={`${props.testIdPrefix}-open-in-popover`}
          >
            <ExternalAppMenuItems
              apps={props.apps.apps}
              preferredId={props.apps.preferredId}
              onSelect={id => props.apps.launch(id, toplevel())}
              onRefresh={() => void props.apps.refresh()}
              refreshing={props.apps.refreshing}
              testIdPrefix={props.testIdPrefix}
            />
          </SubMenu>
        </Show>
      </Show>
    </>
  )
}

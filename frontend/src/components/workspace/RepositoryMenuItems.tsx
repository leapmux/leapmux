import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { revealInFileManager } from '~/api/platformBridge'
import { ExternalAppMenuItems } from '~/components/common/ExternalAppMenuItems'
import { SubMenu } from '~/components/common/SubMenu'
import { useExternalApps } from '~/hooks/useExternalApps'
import { copyTextToClipboard } from '~/lib/clipboard'
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
}

/**
 * The `Repository` section: everything a user can do to one checkout without
 * changing it.
 *
 * Four surfaces render exactly this block -- the workspace row menu, the
 * branch row menu, the repository row menu, and each per-checkout submenu the
 * last two open -- so a user learns it once and knows all four. It was one
 * surface with three items before, and the other three had none.
 *
 * Its test ids are CONSTANT, not per-surface. One block is in the DOM at a
 * time: `RepositoryTargetMenu` puts each target's actions inside a `SubMenu`,
 * which mounts its children only while that submenu is open, and every surface
 * renders no block at all while its own menu is closed. The per-surface prefix
 * this took before was threaded through three components to prevent a
 * collision that cannot occur, and no test read any of its values.
 *
 * Every action is a read, which is why an ARCHIVED workspace keeps the whole
 * block: copying a URL, copying a path, revealing a directory and opening an
 * application all leave the workspace exactly as it was.
 *
 * It stands the application probe up ITSELF rather than taking one as a prop.
 * Three surfaces used to do that at three different levels -- one hoisted for a
 * whole menu, one per checkout, one in a wrapper component whose only job was
 * to hold the hook -- so "where is the probe" had three answers. Here it has
 * one, and it costs nothing extra: every surface renders this block only while
 * its own menu is open, and the detected list itself lives in one module signal
 * that every instance reads.
 */
export const RepositoryMenuItems: Component<RepositoryMenuItemsProps> = (props) => {
  const toplevel = () => props.checkout().gitToplevel

  // Only for a LOCAL checkout: a remote worker's path either does not exist on
  // this machine or is a different directory, so there is nothing here to open.
  const apps = useExternalApps(() => props.checkout().isLocal)

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

        {/* KEPT when the remembered application is the file manager, unlike
            the row above it. "Reveal in file manager" selects the directory
            inside its PARENT; this opens the directory itself. Two different
            operations, so hiding one because the other is next to it left the
            row silently disappearing whenever a user picked Finder once. */}
        <Show when={apps.preferred()}>
          {app => menuItem(`Open in ${app().displayName}`, () => apps.launch(app().id, toplevel()))}
        </Show>

        <Show when={apps.apps().length > 0}>
          <SubMenu
            label="Open in…"
            data-testid="repository-open-in"
            popoverTestId="repository-open-in-popover"
          >
            <ExternalAppMenuItems
              apps={apps.apps}
              preferredId={apps.preferredId}
              onSelect={id => apps.launch(id, toplevel())}
              onRefresh={() => void apps.refresh()}
              refreshing={apps.refreshing}
              testIdPrefix="repository"
            />
          </SubMenu>
        </Show>
      </Show>
    </>
  )
}

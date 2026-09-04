import type { Component } from 'solid-js'
import type { RepoStartPoint } from './repoStartPoints'
import type { RepoGitStore } from '~/stores/repoGit'
import { createMemo, createResource, For, Show } from 'solid-js'
import { platformBridge, revealInFileManager } from '~/api/platformBridge'
import { SubMenu } from '~/components/common/SubMenu'
import { usePreferences } from '~/context/PreferencesContext'
import { copyTextToClipboard } from '~/lib/clipboard'
import { loadDetectedEditors, preferredEditor, resolvePreferredEditor } from '~/lib/externalEditors'
import { createLogger } from '~/lib/logger'
import { repoGitView } from '~/stores/repoGit'
import { menuSectionHeader } from '~/styles/shared.css'
import { menuItem } from './workspaceMenuItem'

const log = createLogger('workspace-repository-menu')

export interface WorkspaceRepositoryMenuProps {
  /** Every repository this workspace has a checkout in. */
  repos: () => readonly RepoStartPoint[]
  repoGitStore: RepoGitStore
  /** Whether a worker runs on THIS machine. See `~/lib/workerLocality`. */
  isLocal: (workerId: string) => boolean
  /** Whether the enclosing menu is open, so the editor probe waits for it. */
  menuOpen: () => boolean
}

/**
 * The read-only actions that hang off a workspace's repositories.
 *
 * Its own component because the editor probe belongs to it and to nothing else:
 * the only consumer of `loadDetectedEditors` here is the "Open in ..." item, so
 * the resource travels with the item rather than being a prop the row menu
 * threads through.
 *
 * Every action here mutates nothing, so the submenu stays for an ARCHIVED
 * workspace: copying a URL, revealing a directory and opening an editor are all
 * reads.
 */
export const WorkspaceRepositoryMenu: Component<WorkspaceRepositoryMenuProps> = (props) => {
  const prefs = usePreferences()

  const anyLocal = () => props.repos().some(r => props.isLocal(r.startPoint.workerId))

  // Fetched on open, and only where a local checkout could use one.
  // `loadDetectedEditors` caches module-wide, so the second row's menu pays
  // nothing.
  const [editors] = createResource(
    () => props.menuOpen() && anyLocal(),
    async (eligible: boolean) => {
      if (!eligible)
        return []
      // Caught here: `initialValue` does NOT suppress a rejection, and Solid
      // re-throws one from the accessor. `namedEditor()` is read inside this
      // menu's JSX, so a sidecar that cannot answer `ListEditors` would replace
      // the whole shell with the route's error boundary instead of hiding one
      // item.
      try {
        return await loadDetectedEditors()
      }
      catch (err: unknown) {
        log.warn('list_editors failed; offering no editor', { err })
        return []
      }
    },
    { initialValue: [] },
  )
  // Named WITHOUT persisting: the label must not write a preference just by
  // rendering. The launch below goes through `resolvePreferredEditor`, which
  // pins whatever it picked.
  const namedEditor = () => preferredEditor(editors(), prefs.preferredEditorId())

  /**
   * Each repository with the two facts its actions depend on.
   *
   * Resolved ONCE per repository. "Does this offer anything" is asked twice --
   * for the submenu itself and again for each row inside it -- and the actions
   * then need the same origin URL a third time. Answering it in one place is
   * also what keeps the submenu's own condition and its rows from disagreeing
   * after a later edit to either.
   */
  const repoRows = createMemo(() => props.repos().map((repo) => {
    const tab = { workerId: repo.startPoint.workerId, gitToplevel: repo.startPoint.gitToplevel }
    return {
      repo,
      originUrl: repoGitView(tab, props.repoGitStore).originUrl,
      local: props.isLocal(repo.startPoint.workerId),
    }
  }))

  type RepoRow = ReturnType<typeof repoRows>[number]

  /** Whether one repository offers any action at all. */
  const hasRepoActions = (row: RepoRow) => Boolean(row.originUrl) || row.local

  const repoActions = (row: RepoRow) => {
    const toplevel = row.repo.startPoint.gitToplevel
    return (
      <>
        {/* Hidden with no origin: there is no URL to copy. */}
        <Show when={row.originUrl}>
          {url => menuItem('Copy repository URL', () => void copyTextToClipboard(url()))}
        </Show>
        {/* Both of these open the LOCAL Finder or the LOCAL editor, so a
            remote worker's absolute path either does not exist here or --
            worse -- exists and is a different directory. */}
        <Show when={row.local}>
          {menuItem('Reveal in file manager', () => void revealInFileManager(toplevel))}
          <Show when={namedEditor()}>
            {editor => menuItem(`Open in ${editor().displayName}`, () => {
              const target = resolvePreferredEditor(editors(), prefs.preferredEditorId(), prefs.setPreferredEditorId)
              if (!target)
                return
              platformBridge.openInEditor(target.id, toplevel).catch((err: unknown) => {
                log.warn('open_in_editor failed', { id: target.id, err })
              })
            })}
          </Show>
        </Show>
      </>
    )
  }

  return (
    <Show when={repoRows().some(hasRepoActions)}>
      <SubMenu label="Repository" data-testid="workspace-repository" popoverTestId="workspace-repository-popover">
        {/* The shape follows the repository COUNT, not the actionable count:
            one repository reads better flat, and a lone group header over a
            single list says nothing. */}
        <Show
          when={repoRows().length > 1}
          fallback={repoActions(repoRows()[0])}
        >
          <For each={repoRows()}>
            {row => (
              <Show when={hasRepoActions(row)}>
                <div role="group" aria-label={row.repo.label}>
                  <div class={menuSectionHeader} aria-hidden="true">{row.repo.label}</div>
                  {repoActions(row)}
                </div>
              </Show>
            )}
          </For>
        </Show>
      </SubMenu>
    </Show>
  )
}

import type { Component } from 'solid-js'
import type { RepoStartPoint } from './repoStartPoints'
import type { WorkspaceStartAt } from './workspaceStartActions'
import { For, Show } from 'solid-js'
import { SubMenu } from '~/components/common/SubMenu'
import { Tooltip } from '~/components/common/Tooltip'
import { menuItem } from './workspaceMenuItem'

export interface WorkspaceStartMenuItemsProps {
  /** The verb the item leads with: "New agent" or "New terminal". */
  'verb': string
  /** Every repository this workspace has a checkout in. */
  'repos': () => readonly RepoStartPoint[]
  /** Those on a reachable worker -- the ones a tab can actually open in. */
  'startableRepos': () => readonly RepoStartPoint[]
  'run': (at: WorkspaceStartAt) => void
  /** The no-target start: this workspace, and no checkout. */
  'startAtWorkspace': () => WorkspaceStartAt
  'startAt': (repo: RepoStartPoint) => WorkspaceStartAt
  'data-testid': string
}

/**
 * One tab-creation entry, in the shape the repository count calls for.
 *
 * Three shapes, and the third state is the one worth naming:
 *
 *  - NO checkout: the item still opens, with no target. A freshly created
 *    workspace has no tabs, and that is exactly the row that most needs a way
 *    in. An empty worker and directory mean "follow the current tab context".
 *  - Checkouts, none REACHABLE: the item is disabled and says why. It cannot
 *    borrow the no-target shape, because "follow the current tab context"
 *    would start an agent on a machine the user never picked.
 *  - One reachable checkout renders FLAT; more than one opens a submenu. A
 *    submenu holding a single item is a click the user should not have to make.
 */
export const WorkspaceStartMenuItems: Component<WorkspaceStartMenuItemsProps> = (props) => {
  const allTargetsOffline = () => props.repos().length > 0 && props.startableRepos().length === 0

  return (
    <Show
      when={!allTargetsOffline()}
      fallback={(
        <Tooltip text="Every machine this workspace is checked out on is offline.">
          <button type="button" role="menuitem" data-testid={props['data-testid']} disabled>
            {`${props.verb}...`}
          </button>
        </Tooltip>
      )}
    >
      <Show
        when={props.startableRepos().length > 0}
        fallback={menuItem(
          `${props.verb}...`,
          () => props.run(props.startAtWorkspace()),
          props['data-testid'],
        )}
      >
        <Show
          when={props.startableRepos().length > 1}
          fallback={menuItem(
            `${props.verb} in ${props.startableRepos()[0].label}...`,
            () => props.run(props.startAt(props.startableRepos()[0])),
            props['data-testid'],
          )}
        >
          <SubMenu
            label={`${props.verb} in`}
            data-testid={props['data-testid']}
            popoverTestId={`${props['data-testid']}-popover`}
          >
            <For each={props.startableRepos()}>
              {repo => menuItem(repo.label, () => props.run(props.startAt(repo)))}
            </For>
          </SubMenu>
        </Show>
      </Show>
    </Show>
  )
}

import type { Component } from 'solid-js'
import type { IconSizeName } from '~/components/common/Icon'
import type { DiffStats } from '~/stores/repoGit'
import FolderSymlink from 'lucide-solid/icons/folder-symlink'
import GitBranch from 'lucide-solid/icons/git-branch'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { tildify } from '~/lib/paths'
import * as styles from './WorkingTree.css'

/**
 * One vocabulary for the two kinds of checkout LeapMux opens tabs in.
 *
 * Git's own terms: a repository has ONE main working tree -- the repository's
 * own directory -- and zero or more LINKED working trees that `git worktree
 * add` creates. `isWorktree` is true for a linked one. The words this app shows
 * the user are `Worktree` for a linked working tree and `Branch` for the main
 * one, because "branch" is what a user perceives when a repository has only the
 * one directory.
 *
 * Every surface that names a checkout reads the icon, the noun and the rows
 * from here: the sidebar branch row, the composer chip, the composer `[+]`
 * menu, the branch context menu, both branch dialogs and the agent info card.
 * A second spelling anywhere is how one surface starts calling a worktree a
 * branch while its neighbour does not.
 */

/** True when the checkout is a linked worktree rather than the main working tree. */
export interface WorkingTreeKind {
  isWorktree: boolean
}

/**
 * The noun for a heading, a menu item or a sentence.
 *
 * Title case, because every call site so far starts a label or a row heading
 * with it. A caller that needs it mid-sentence lowercases it there.
 */
export function workingTreeKindLabel(isWorktree: boolean): string {
  return isWorktree ? 'Worktree' : 'Branch'
}

export interface WorkingTreeIconProps extends WorkingTreeKind {
  size: IconSizeName
  class?: string
}

/**
 * The glyph that tells the two kinds apart at a glance.
 *
 * `FolderSymlink` for a linked worktree, because that is literally what one is
 * -- a directory whose `.git` is a file pointing back into the main repository.
 * `GitBranch` stays with the main working tree. The repo group row above them
 * uses `FolderGit`, which neither of these can be mistaken for.
 */
export const WorkingTreeIcon: Component<WorkingTreeIconProps> = props => (
  <Icon
    icon={props.isWorktree ? FolderSymlink : GitBranch}
    size={props.size}
    class={props.class}
    data-testid={props.isWorktree ? 'worktree-icon' : 'branch-icon'}
  />
)

export interface WorkingTreeRowsProps extends WorkingTreeKind {
  /**
   * The branch name, or the caller's own placeholder for a checkout with no
   * branch (the sidebar's `(no branch)` bucket). Empty renders an empty value
   * rather than dropping the row, so the label column still states the kind.
   */
  name: string
  /** Absolute working-tree root. Rendered tilde-compressed against `homeDir`. */
  directory: string
  /**
   * The worker's home directory, for the tilde compression. Absent or empty
   * leaves the path absolute, which is what `tildify` already does -- so a
   * worker whose system info has not landed yet shows a correct long path
   * rather than a wrong short one.
   */
  homeDir?: string
  /** Diff badge beside the name. Omit it where the caller has no stats. */
  stats?: DiffStats | null
}

/**
 * The labelled rows that state which kind of checkout this is and where it
 * lives:
 *
 *     Worktree    blushing-slow-wolf              +38 -12
 *     Directory   ~/Workspaces/leapmux-worktrees/blushing-slow-wolf
 *
 * It renders NO interactive child and NO nested `Tooltip`. Most callers pass it
 * as `Tooltip`'s `content`, and a tooltip's portal sets `pointer-events: none`
 * over everything inside it -- so a button or a nested tooltip there can never
 * be reached. A caller that wants a copy affordance builds its own rows; see
 * `AgentInfoCard`, which keeps its three-column grid for exactly that reason.
 */
export const WorkingTreeRows: Component<WorkingTreeRowsProps> = props => (
  <div class={styles.rows} data-testid="working-tree-rows">
    <span class={styles.label}>{workingTreeKindLabel(props.isWorktree)}</span>
    <span class={styles.kindValue}>
      {/* The icon repeats what the label column already says, on purpose: this
          is where a reader learns which glyph the row it hovered is using. */}
      <WorkingTreeIcon isWorktree={props.isWorktree} size="xs" />
      <span data-testid="working-tree-name">{props.name}</span>
      <Show when={props.stats}>
        {s => <DiffStatsBadge stats={s()} />}
      </Show>
    </span>
    <span class={styles.label}>Directory</span>
    <span class={styles.pathValue} data-testid="working-tree-directory">
      {tildify(props.directory, props.homeDir)}
    </span>
  </div>
)

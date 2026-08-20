import type { Component } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { tildify } from '~/lib/paths'

export interface WorktreeOption {
  path: string
  branch: string
}

interface WorktreeSelectProps {
  value: string
  onChange: (value: string) => void
  worktrees: WorktreeOption[]
  loading: boolean
  /** Home directory used to abbreviate the worktree path with `~/`. */
  homeDir?: string
}

export const WorktreeSelect: Component<WorktreeSelectProps> = props => (
  <LoadingMenu
    ariaLabel="Worktree"
    value={props.value}
    onChange={props.onChange}
    loadingLabel={props.loading ? 'Loading worktrees...' : undefined}
    emptyLabel="No worktrees found"
    placeholder="Select a worktree..."
    options={props.worktrees.map(wt => ({
      value: wt.path,
      label: `${wt.branch ? `${wt.branch} — ` : ''}${tildify(wt.path, props.homeDir)}`,
    }))}
    data-testid="worktree-select-menu"
  />
)

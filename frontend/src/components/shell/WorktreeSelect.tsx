import type { Component } from 'solid-js'
import { For } from 'solid-js'
import { LoadingSelect } from '~/components/common/LoadingSelect'
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
  <LoadingSelect
    value={props.value}
    onChange={props.onChange}
    loading={props.loading}
    isEmpty={props.worktrees.length === 0}
    loadingLabel="Loading worktrees..."
    emptyLabel="No worktrees found"
  >
    <option value="">Select a worktree...</option>
    <For each={props.worktrees}>
      {wt => (
        <option value={wt.path}>
          {wt.branch ? `${wt.branch} — ` : ''}
          {tildify(wt.path, props.homeDir)}
        </option>
      )}
    </For>
  </LoadingSelect>
)

import type { Component } from 'solid-js'
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { For, Show } from 'solid-js'
import { LoadingSelect } from '~/components/common/LoadingSelect'

/**
 * Walk `branches` once, separating local from remote entries. Exported
 * so callers that only hold a single list (e.g. DeleteBranchDialog) can
 * partition once at the call site and feed BranchSelect the already-split
 * arrays — avoiding a second walk inside the component every render. The
 * input order is preserved within each output array.
 */
export function partitionBranches(branches: readonly GitBranchEntry[]): {
  local: GitBranchEntry[]
  remote: GitBranchEntry[]
} {
  const local: GitBranchEntry[] = []
  const remote: GitBranchEntry[] = []
  for (const b of branches) {
    if (b.isRemote)
      remote.push(b)
    else
      local.push(b)
  }
  return { local, remote }
}

interface BranchSelectProps {
  value: string
  onChange: (value: string) => void
  /** Local branches, in display order. */
  local: GitBranchEntry[]
  /** Remote branches, in display order. */
  remote: GitBranchEntry[]
  loading?: boolean
  currentBranch?: string
  showPrompt?: boolean
  showCurrent?: boolean
  disabled?: boolean
}

export const BranchSelect: Component<BranchSelectProps> = (props) => {
  return (
    <LoadingSelect
      value={props.value}
      onChange={props.onChange}
      loading={props.loading ?? false}
      isEmpty={props.local.length === 0 && props.remote.length === 0}
      loadingLabel="Loading branches..."
      emptyLabel="No branches found"
      disabled={props.disabled}
    >
      <Show when={props.showPrompt}>
        <option value="">Select a branch...</option>
      </Show>
      <Show when={props.local.length > 0}>
        <optgroup label="Local">
          <For each={props.local}>
            {b => (
              <option value={b.name}>
                {b.name}
                {props.showCurrent && b.name === props.currentBranch ? ' (current)' : ''}
              </option>
            )}
          </For>
        </optgroup>
      </Show>
      <Show when={props.remote.length > 0}>
        <optgroup label="Remote">
          <For each={props.remote}>
            {b => <option value={b.name}>{b.name}</option>}
          </For>
        </optgroup>
      </Show>
    </LoadingSelect>
  )
}

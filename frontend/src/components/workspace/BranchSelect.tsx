import type { Component } from 'solid-js'
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { createMemo } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'

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
  showCurrent?: boolean
  disabled?: boolean
}

export const BranchSelect: Component<BranchSelectProps> = (props) => {
  // `<optgroup>` became a `group` on each entry; the menu draws a heading
  // whenever it changes, so Local and Remote still read apart.
  //
  // MEMOIZED. A repository's branch list is unbounded, so this menu carries a
  // filter box -- and `LoadingMenu`'s `visible` memo re-reads
  // `options` on every keystroke. A plain function handed `<For>` a fresh object
  // for every branch each time, and `<For>` reconciles by reference, so each
  // character typed tore down and rebuilt every row instead of hiding the ones
  // that stopped matching. With stable identities `.filter` preserves the
  // survivors and only the non-matching rows leave.
  // NO "Select a branch..." ROW. It used to be injected here when `showPrompt`
  // was set, and it was the same string this component already passes as
  // `placeholder` -- so the prompt showed twice, once on the trigger and once
  // as a selectable row whose only effect was to put the value back to "". It
  // also made `options` one entry long in a repository with NO branches, which
  // is why `LoadingMenu` could not derive its own empty state until it went.
  const options = createMemo(() => [
    ...props.local.map(b => ({
      value: b.name,
      label: props.showCurrent && b.name === props.currentBranch ? `${b.name} (current)` : b.name,
      group: 'Local',
    })),
    ...props.remote.map(b => ({ value: b.name, label: b.name, group: 'Remote' })),
  ])

  return (
    <LoadingMenu
      ariaLabel="Branch"
      value={props.value}
      onChange={props.onChange}
      loadingLabel={props.loading ? 'Loading branches...' : undefined}
      emptyLabel="No branches found"
      placeholder="Select a branch..."
      disabled={props.disabled}
      options={options()}
      // A repository's branch list is unbounded, and a native select gave
      // type-ahead over it for free. This is what buys that back.
      // STATED, although `LoadingMenu` would derive it from the count anyway: a
      // repository's branch list is unbounded, so this menu wants the box even
      // in a repository that happens to hold three branches today.
      filter
      data-testid="branch-select-menu"
    />
  )
}

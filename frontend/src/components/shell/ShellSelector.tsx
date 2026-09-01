import type { Accessor, Component } from 'solid-js'
import { LabeledField } from '~/components/common/LabeledField'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { RefreshButton } from '~/components/common/RefreshButton'

/**
 * Narrow slice of {@link useAvailableShells}' result that this component
 * reads. Defined here for the reason WorkerSelectorState states: adding a
 * field to the hook does not silently reach into this component, and a unit
 * test can pass a stub matching just this shape.
 */
export interface ShellSelectorState {
  shells: Accessor<string[]>
  defaultShell: Accessor<string>
  shell: Accessor<string>
  setShell: (v: string | null) => void
  loading: Accessor<boolean>
  refresh: () => Promise<void> | void
}

interface ShellSelectorProps {
  state: ShellSelectorState
}

/**
 * The Shell field: LabeledField's frame around the shell menu, so it is laid
 * out exactly as WorkerSelector lays out the Worker field.
 *
 * It used to be a bare `<label>` wrapping the menu, which took Oat's
 * `label { font-size: var(--text-7); font-weight: var(--font-medium) }` rule
 * and typeset "Shell" differently from "Worker" beside it, and left no slot
 * for a trailing button. ChangeBranchDialog rendered a third variant. That
 * rule now lives on LabeledField, where the element is chosen.
 *
 * The refresh button is the only route to `useAvailableShells().refresh()`.
 * The hook's own effect re-fetches on a workerId TRANSITION, so a transient
 * failure against the current worker left the dialog with an empty shell list,
 * a disabled Create button, and no way back except picking a different worker
 * and returning.
 */
export const ShellSelector: Component<ShellSelectorProps> = props => (
  <LabeledField
    label="Shell"
    actions={(
      <RefreshButton
        onClick={() => void props.state.refresh()}
        disabled={props.state.loading()}
        title="Refresh shells"
        data-testid="shell-selector-refresh"
      />
    )}
  >
    <LoadingMenu
      ariaLabel="Shell"
      value={props.state.shell()}
      onChange={props.state.setShell}
      loadingLabel={props.state.loading() ? 'Loading shells...' : undefined}
      emptyLabel="No shells available"
      options={props.state.shells().map(s => ({
        value: s,
        label: s === props.state.defaultShell() ? `${s} (default)` : s,
      }))}
      data-testid="shell-select-menu"
    />
  </LabeledField>
)

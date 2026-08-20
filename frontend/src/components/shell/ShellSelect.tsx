import type { Component } from 'solid-js'
import { LoadingMenu } from '~/components/common/LoadingMenu'

interface ShellSelectProps {
  value: string
  onChange: (value: string) => void
  shells: string[]
  defaultShell: string
  loading: boolean
}

export const ShellSelect: Component<ShellSelectProps> = props => (
  <LoadingMenu
    ariaLabel="Shell"
    value={props.value}
    onChange={props.onChange}
    loadingLabel={props.loading ? 'Loading shells...' : undefined}
    emptyLabel="No shells available"
    options={props.shells.map(s => ({
      value: s,
      label: s === props.defaultShell ? `${s} (default)` : s,
    }))}
    data-testid="shell-select-menu"
  />
)

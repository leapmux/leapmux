import type { Component } from 'solid-js'
import { For } from 'solid-js'
import { LoadingSelect } from '~/components/common/LoadingSelect'

interface ShellSelectProps {
  value: string
  onChange: (value: string) => void
  shells: string[]
  defaultShell: string
  loading: boolean
}

export const ShellSelect: Component<ShellSelectProps> = props => (
  <LoadingSelect
    value={props.value}
    onChange={props.onChange}
    loading={props.loading}
    isEmpty={props.shells.length === 0}
    loadingLabel="Loading shells..."
    emptyLabel="No shells available"
  >
    <For each={props.shells}>
      {s => (
        <option value={s}>
          {s === props.defaultShell ? `${s} (default)` : s}
        </option>
      )}
    </For>
  </LoadingSelect>
)

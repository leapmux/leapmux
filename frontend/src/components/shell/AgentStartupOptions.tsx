import type { StartupOptionGroup } from '../chat/providers/capabilities'
import { For, Show } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { LabeledField } from '~/components/common/LabeledField'
import { PillGroup } from '~/components/common/PillGroup'
import { isPillOptions } from '~/components/common/pillOptions'
import { resolveStartupOptionValues } from '../chat/providers/startupOptions'

export function AgentStartupOptions(props: {
  groups: readonly StartupOptionGroup[]
  selected: Readonly<Record<string, string>>
  onChange: (id: string, value: string) => void
  disabled?: boolean
}) {
  const selected = () => resolveStartupOptionValues(props.groups, props.selected)
  const pills = (group: StartupOptionGroup) => {
    const options = group.options.map(option => ({ key: option.value, label: option.label }))
    return isPillOptions(options) ? options : undefined
  }
  return (
    <For each={props.groups}>
      {group => (
        <LabeledField label={group.label}>
          <Show
            when={pills(group)}
            fallback={(
              <DropdownMenu trigger={trigger => (
                <button type="button" disabled={props.disabled} {...trigger}>
                  {group.options.find(option => option.value === selected()[group.id])?.label}
                </button>
              )}
              >
                <For each={group.options}>
                  {option => (
                    <DropdownMenuCheckableItem kind="radio" label={option.label} checked={option.value === selected()[group.id]} onSelect={() => props.onChange(group.id, option.value)} />
                  )}
                </For>
              </DropdownMenu>
            )}
          >
            {options => <PillGroup label={group.label} options={options()} selectedKey={selected()[group.id] ?? group.defaultValue} onSelect={value => props.onChange(group.id, value)} disabled={props.disabled === true} />}
          </Show>
        </LabeledField>
      )}
    </For>
  )
}

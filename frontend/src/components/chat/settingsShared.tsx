import type { JSX } from 'solid-js'
import type { AvailableModel, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { For } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './ChatView.css'

/** Shared item type used by RadioGroup and settings helpers. */
export interface SettingsItem {
  label: string
  value: string
  tooltip?: string
}

/** Build model radio items from available models. */
export function modelItems(availableModels: AvailableModel[] | undefined): SettingsItem[] {
  if (availableModels && availableModels.length > 0)
    return availableModels.map(m => ({ label: m.displayName || m.id, value: m.id, tooltip: m.description || undefined }))
  return []
}

/** Build effort radio items for the current model. */
export function effortItems(availableModels: AvailableModel[] | undefined, currentModel: string): SettingsItem[] {
  if (availableModels && availableModels.length > 0) {
    const model = availableModels.find(m => m.id === currentModel)
    if (model)
      return model.supportedEfforts.map(e => ({ label: e.name || e.id, value: e.id, tooltip: e.description || undefined }))
  }
  return []
}

/** Find the permission mode option group. */
export function permissionModeGroup(availableOptionGroups: AvailableOptionGroup[] | undefined) {
  return availableOptionGroups?.find(g => g.key === 'permissionMode')
}

/** Build permission mode radio items. */
export function permissionModeItems(availableOptionGroups: AvailableOptionGroup[] | undefined): SettingsItem[] {
  const group = permissionModeGroup(availableOptionGroups)
  if (group && group.options.length > 0)
    return group.options.map(o => ({ label: o.name || o.id, value: o.id, tooltip: o.description || undefined }))
  return []
}

/** Resolve model display name from available models. */
export function modelDisplayName(availableModels: AvailableModel[] | undefined, currentModel: string): string {
  if (availableModels && availableModels.length > 0) {
    const model = availableModels.find(m => m.id === currentModel)
    if (model)
      return model.displayName || model.id
  }
  return currentModel
}

/** Check if the current model has efforts. */
export function hasEfforts(availableModels: AvailableModel[] | undefined, currentModel: string): boolean {
  if (availableModels && availableModels.length > 0) {
    const model = availableModels.find(m => m.id === currentModel)
    return model ? model.supportedEfforts.length > 0 : false
  }
  return false
}

/** Resolve permission mode label from available option groups. */
export function modeLabel(availableOptionGroups: AvailableOptionGroup[] | undefined, currentMode: string): string {
  const group = availableOptionGroups?.find(g => g.key === 'permissionMode')
  if (group) {
    const opt = group.options.find(o => o.id === currentMode)
    if (opt)
      return opt.name || opt.id
  }
  return currentMode
}

export function RadioGroup(props: {
  label: string
  items: { label: string, value: string, tooltip?: string }[]
  testIdPrefix: string
  name: string
  current: string
  onChange: (value: string) => void
  fieldsetClass?: string
}): JSX.Element {
  return (
    <fieldset class={[styles.settingsFieldset, props.fieldsetClass].filter(Boolean).join(' ')}>
      <legend class={styles.settingsGroupLabel}>{props.label}</legend>
      <For each={props.items}>
        {item => (
          <Tooltip text={item.tooltip}>
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`${props.testIdPrefix}-${item.value}`}
            >
              <input
                type="radio"
                name={props.name}
                value={item.value}
                checked={props.current === item.value}
                onChange={() => props.onChange(item.value)}
              />
              {item.label}
            </label>
          </Tooltip>
        )}
      </For>
    </fieldset>
  )
}

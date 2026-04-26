import type { JSX } from 'solid-js'
import type { ProviderSettingsPanelProps } from '../registry'
import type { PermissionMode } from '~/utils/controlResponse'
import { createUniqueId, Show } from 'solid-js'
import * as styles from '../../ChatView.css'
import {
  defaultModelId,
  modelDisplayName,
  modelItems,
  ModelSelect,
  optionGroup,
  optionGroupItems,
  optionLabel,
  PERMISSION_MODE_KEY,
  permissionModeGroup,
  permissionModeItems,
  RadioGroup,
} from '../../settingsShared'

export interface ACPSettingsPanelConfig {
  defaultModel: string
  /** Option group key — 'permissionMode' for Copilot/Gemini, or a custom key like 'primaryAgent'. */
  optionGroupKey: string
  defaultOptionValue: string
  fallbackLabel: string
  testIdPrefix: string
}

/** Read the current option value from props based on the option group key. */
function resolveCurrentOption(props: ProviderSettingsPanelProps, config: ACPSettingsPanelConfig): string {
  if (config.optionGroupKey === PERMISSION_MODE_KEY)
    return props.permissionMode || config.defaultOptionValue
  return props.extraSettings?.[config.optionGroupKey] || config.defaultOptionValue
}

/** Dispatch an option change via the appropriate callback. */
function dispatchOptionChange(props: ProviderSettingsPanelProps, config: ACPSettingsPanelConfig, value: string): void {
  if (config.optionGroupKey === PERMISSION_MODE_KEY)
    props.onPermissionModeChange?.(value as PermissionMode)
  else
    props.onOptionGroupChange?.(config.optionGroupKey, value)
}

export function createACPSettingsPanel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const isPermissionMode = config.optionGroupKey === PERMISSION_MODE_KEY
    const menuId = createUniqueId()
    const currentModel = () => props.model || defaultModelId(props.availableModels) || config.defaultModel
    const currentOption = () => resolveCurrentOption(props, config)
    const models = () => modelItems(props.availableModels)

    const optGroup = () => isPermissionMode
      ? permissionModeGroup(props.availableOptionGroups)
      : optionGroup(props.availableOptionGroups, config.optionGroupKey)
    const optItems = () => isPermissionMode
      ? permissionModeItems(props.availableOptionGroups)
      : optionGroupItems(props.availableOptionGroups, config.optionGroupKey)

    return (
      <>
        <Show when={optItems().length > 0}>
          <RadioGroup
            label={optGroup()?.label || config.fallbackLabel}
            items={optItems()}
            testIdPrefix={config.testIdPrefix}
            name={`${menuId}-${config.testIdPrefix}`}
            current={currentOption()}
            onChange={v => dispatchOptionChange(props, config, v)}
            fieldsetClass={styles.settingsFieldsetFirst}
          />
        </Show>
        <Show when={models().length > 0}>
          <ModelSelect
            items={models()}
            testIdPrefix="model"
            name={`${menuId}-model`}
            current={currentModel()}
            onChange={v => props.onModelChange?.(v)}
            fieldsetClass={optItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
          />
        </Show>
      </>
    )
  }
}

export function createACPTriggerLabel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const currentModel = () => props.model || defaultModelId(props.availableModels) || config.defaultModel
    const currentOption = () => resolveCurrentOption(props, config)
    return (
      <>
        {modelDisplayName(props.availableModels, currentModel())}
        {' · '}
        {optionLabel(props.availableOptionGroups, config.optionGroupKey, currentOption())}
      </>
    )
  }
}

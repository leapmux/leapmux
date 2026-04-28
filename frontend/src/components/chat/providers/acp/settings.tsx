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

interface CommonSettingsConfig {
  defaultModel: string
  fallbackLabel: string
  testIdPrefix: string
}

/**
 * Configures the shared ACP settings panel. The discriminator picks the
 * read/write path: ACP providers that store the toggle in the agent's
 * top-level `permissionMode` field (Copilot, Cursor, Gemini, Goose) use
 * `kind: 'permissionMode'`; providers that store it in `extraSettings`
 * under a custom key (OpenCode `primaryAgent`, Kilo) use `kind: 'optionGroup'`.
 */
export type ACPSettingsPanelConfig
  = | (CommonSettingsConfig & { kind: 'permissionMode', defaultMode: PermissionMode })
    | (CommonSettingsConfig & { kind: 'optionGroup', optionGroupKey: string, defaultValue: string })

/** The option-group key as understood by the available-option-groups RPC. */
function optionGroupKeyOf(config: ACPSettingsPanelConfig): string {
  return config.kind === 'permissionMode' ? PERMISSION_MODE_KEY : config.optionGroupKey
}

export function createACPSettingsPanel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const menuId = createUniqueId()
    const currentModel = () => props.model || defaultModelId(props.availableModels) || config.defaultModel
    const currentOption = () => config.kind === 'permissionMode'
      ? props.permissionMode || config.defaultMode
      : props.extraSettings?.[config.optionGroupKey] || config.defaultValue
    const dispatchOption = (value: string) => {
      if (config.kind === 'permissionMode')
        props.onChange?.({ kind: 'permissionMode', value: value as PermissionMode })
      else
        props.onChange?.({ kind: 'optionGroup', key: config.optionGroupKey, value })
    }
    const models = () => modelItems(props.availableModels)
    const optGroup = () => config.kind === 'permissionMode'
      ? permissionModeGroup(props.availableOptionGroups)
      : optionGroup(props.availableOptionGroups, config.optionGroupKey)
    const optItems = () => config.kind === 'permissionMode'
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
            onChange={dispatchOption}
            fieldsetClass={styles.settingsFieldsetFirst}
          />
        </Show>
        <Show when={models().length > 0}>
          <ModelSelect
            items={models()}
            testIdPrefix="model"
            name={`${menuId}-model`}
            current={currentModel()}
            onChange={v => props.onChange?.({ kind: 'model', value: v })}
            fieldsetClass={optItems().length === 0 ? styles.settingsFieldsetFirst : undefined}
          />
        </Show>
      </>
    )
  }
}

export function createACPTriggerLabel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  const groupKey = optionGroupKeyOf(config)
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const currentModel = () => props.model || defaultModelId(props.availableModels) || config.defaultModel
    const currentOption = () => config.kind === 'permissionMode'
      ? props.permissionMode || config.defaultMode
      : props.extraSettings?.[config.optionGroupKey] || config.defaultValue
    return (
      <>
        {modelDisplayName(props.availableModels, currentModel())}
        {' · '}
        {optionLabel(props.availableOptionGroups, groupKey, currentOption())}
      </>
    )
  }
}

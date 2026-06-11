import type { JSX } from 'solid-js'
import type { ProviderSettingsPanelProps } from '../registry'
import type { PermissionMode } from '~/utils/controlResponse'
import { createUniqueId, For, Show } from 'solid-js'
import * as styles from '../../ChatView.css'
import {
  defaultModelId,
  modelDisplayName,
  modelItems,
  ModelSelect,
  optionGroup,
  optionGroupDefaultValue,
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
 * top-level `permissionMode` field (Copilot, Cursor, Goose) use
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

/** Resolve the current model id, falling back through the available default then the config default. */
function currentModelOf(props: ProviderSettingsPanelProps, config: ACPSettingsPanelConfig): string {
  return props.model || defaultModelId(props.availableModels) || config.defaultModel
}

/**
 * Resolve the current mapped-group value (permission mode or extraSettings option),
 * falling back to the config default. Shared by the panel and the trigger label so the
 * two can't drift in how they resolve the current selection.
 */
function currentOptionOf(props: ProviderSettingsPanelProps, config: ACPSettingsPanelConfig): string {
  return config.kind === 'permissionMode'
    ? props.permissionMode || config.defaultMode
    : props.extraSettings?.[config.optionGroupKey] || config.defaultValue
}

export function createACPSettingsPanel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const menuId = createUniqueId()
    const currentModel = () => currentModelOf(props, config)
    const currentOption = () => currentOptionOf(props, config)
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
    // The mapped (writable) group. Every other non-empty group is a generic
    // config-option axis the backend surfaced read-only (e.g. a future
    // thought_level), rendered disabled after the model selector. Single-group
    // providers yield an empty list -> the <For> renders nothing -> identical DOM.
    const primaryKey = optionGroupKeyOf(config)
    const extraGroups = () => (props.availableOptionGroups ?? [])
      .filter(g => g.key !== primaryKey && g.options.length > 0)

    // A single flex column supplies the inter-group gap (var(--space-4)), the
    // same mechanism the Codex/Claude panels use. Without this wrapper the
    // fieldsets would stack flush as bare children of `.settingsMenu`, which has
    // no flex/gap of its own.
    return (
      <div class={styles.settingsPanelColumn}>
        <Show when={optItems().length > 0}>
          <RadioGroup
            label={optGroup()?.label || config.fallbackLabel}
            items={optItems()}
            testIdPrefix={config.testIdPrefix}
            name={`${menuId}-${config.testIdPrefix}`}
            current={currentOption()}
            onChange={dispatchOption}
          />
        </Show>
        <Show when={models().length > 0}>
          <ModelSelect
            items={models()}
            testIdPrefix="model"
            name={`${menuId}-model`}
            current={currentModel()}
            onChange={v => props.onChange?.({ kind: 'model', value: v })}
          />
        </Show>
        <For each={extraGroups()}>
          {group => (
            <RadioGroup
              label={group.label || group.key}
              items={optionGroupItems(props.availableOptionGroups, group.key)}
              testIdPrefix={`extra-${group.key}`}
              name={`${menuId}-extra-${group.key}`}
              current={props.extraSettings?.[group.key] || optionGroupDefaultValue(props.availableOptionGroups, group.key)}
              onChange={() => {}}
              disabled
              disabledReason="This setting is controlled by the agent"
            />
          )}
        </For>
      </div>
    )
  }
}

export function createACPTriggerLabel(config: ACPSettingsPanelConfig): (props: ProviderSettingsPanelProps) => JSX.Element {
  const groupKey = optionGroupKeyOf(config)
  return (props: ProviderSettingsPanelProps): JSX.Element => {
    const currentModel = () => currentModelOf(props, config)
    const currentOption = () => currentOptionOf(props, config)
    return (
      <>
        {modelDisplayName(props.availableModels, currentModel())}
        {' · '}
        {optionLabel(props.availableOptionGroups, groupKey, currentOption())}
      </>
    )
  }
}

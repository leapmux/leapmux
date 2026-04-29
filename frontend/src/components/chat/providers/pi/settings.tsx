import type { JSX } from 'solid-js'
import type { ProviderSettingsPanelProps } from '../registry'
import { createUniqueId, Show } from 'solid-js'
import { EFFORT_AUTO } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import {
  defaultModelId,
  effortIcon,
  effortItems,
  hasEfforts,
  modelDisplayName,
  modelItems,
  ModelSelect,
  RadioGroup,
} from '../../settingsShared'

/** Default model — overridden by env var at build time, fallback to gpt-5.5. */
export const DEFAULT_PI_MODEL = import.meta.env.LEAPMUX_PI_DEFAULT_MODEL || 'gpt-5.5'
export const DEFAULT_PI_EFFORT = EFFORT_AUTO

/** Pi settings panel: model + thinking level. */
export function PiSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_PI_MODEL
  const currentEffort = () => props.effort || DEFAULT_PI_EFFORT

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())

  return (
    <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnPrimary].join(' ')}>
      <RadioGroup
        label="Thinking Level"
        items={efforts()}
        testIdPrefix="effort"
        name={`${menuId}-effort`}
        current={currentEffort()}
        onChange={v => props.onChange?.({ kind: 'effort', value: v })}
        fieldsetClass={styles.settingsFieldsetFirst}
      />
      <ModelSelect
        items={models()}
        testIdPrefix="model"
        name={`${menuId}-model`}
        current={currentModel()}
        onChange={v => props.onChange?.({ kind: 'model', value: v })}
      />
    </div>
  )
}

/** Pi trigger label (model name + thinking level icon). */
export function PiTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_PI_MODEL
  const currentEffort = () => props.effort || DEFAULT_PI_EFFORT
  const displayName = () => modelDisplayName(props.availableModels, currentModel())
  const hasEffort = () => hasEfforts(props.availableModels, currentModel())

  return (
    <>
      {displayName()}
      <Show when={hasEffort()}>{effortIcon(currentEffort())}</Show>
    </>
  )
}

import type { JSX } from 'solid-js'
import type { ProviderSettingsPanelProps } from '../registry'
import { createUniqueId, Show } from 'solid-js'
import { EFFORT_AUTO } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import {
  defaultModelId,
  effortIcon,
  effortItems,
  effortValidForModel,
  effortValueForModel,
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
  // During an optimistic model switch the effort can briefly be a tier the new
  // model doesn't offer; effortValueForModel falls back to Auto so the
  // RadioGroup never renders with no selection (mirroring the trigger label
  // hiding its icon in the same window).
  const effortValue = () => effortValueForModel(props.availableModels, currentModel(), currentEffort())

  return (
    <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnPrimary].join(' ')}>
      <RadioGroup
        label="Thinking Level"
        items={efforts()}
        testIdPrefix="effort"
        name={`${menuId}-effort`}
        current={effortValue()}
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
  // Only show the effort icon when the current effort is one of the current
  // model's tiers. Pi populates SupportedEfforts per-model at runtime, so an
  // optimistic model switch can briefly leave an effort the new model doesn't
  // offer; showing its icon then would contradict the effort RadioGroup.
  const currentEffortValid = () => effortValidForModel(props.availableModels, currentModel(), currentEffort())

  return (
    <>
      {displayName()}
      <Show when={currentEffortValid()}>{effortIcon(currentEffort())}</Show>
    </>
  )
}

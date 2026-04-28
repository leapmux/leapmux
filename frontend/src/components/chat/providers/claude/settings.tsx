import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { ProviderSettingsPanelProps } from '../registry'
import Flame from 'lucide-solid/icons/flame'
import Zap from 'lucide-solid/icons/zap'
import { createUniqueId, Show } from 'solid-js'
import { EFFORT_AUTO } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import { effortIcon, effortItems, hasEfforts, modelDisplayName, modelItems, ModelSelect, optionGroup, optionGroupDefaultValue, optionGroupItems, optionLabel, PERMISSION_MODE_KEY, permissionModeGroup, permissionModeItems, RadioGroup } from '../../settingsShared'

// Claude swaps the default xhigh→Zap mapping for Flame to free up Zap for
// its `max` tier (an effort level Pi/Codex don't expose).
const CLAUDE_EFFORT_ICONS: Record<string, LucideIcon> = {
  xhigh: Flame,
  max: Zap,
}

/** Option group keys for Claude Code-specific settings. */
export const OUTPUT_STYLE_KEY = 'outputStyle' as const
export const FAST_MODE_KEY = 'fastMode' as const
export const ALWAYS_THINKING_KEY = 'alwaysThinkingEnabled' as const

export const DEFAULT_CLAUDE_MODEL = import.meta.env.LEAPMUX_CLAUDE_DEFAULT_MODEL || 'opus[1m]'
// LEAPMUX_CLAUDE_DEFAULT_EFFORT still exists as a backend escape hatch;
// it's no longer plumbed through the frontend build.
export const DEFAULT_CLAUDE_EFFORT = EFFORT_AUTO

/** Claude Code settings panel (two-column: left = thinking/effort/model, right = fast mode/output style/permission mode). */
export function ClaudeCodeSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const currentModel = () => props.model || DEFAULT_CLAUDE_MODEL
  const currentEffort = () => props.effort || DEFAULT_CLAUDE_EFFORT
  const currentMode = () => props.permissionMode || 'default'
  const currentOutputStyle = () => props.extraSettings?.[OUTPUT_STYLE_KEY] || optionGroupDefaultValue(props.availableOptionGroups, OUTPUT_STYLE_KEY) || 'default'
  const currentFastMode = () => props.extraSettings?.[FAST_MODE_KEY] || optionGroupDefaultValue(props.availableOptionGroups, FAST_MODE_KEY) || 'off'
  const currentThinking = () => props.extraSettings?.[ALWAYS_THINKING_KEY] || optionGroupDefaultValue(props.availableOptionGroups, ALWAYS_THINKING_KEY)

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())
  const hasEffort = () => efforts().length > 0
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)
  const outputStyleItems = () => optionGroupItems(props.availableOptionGroups, OUTPUT_STYLE_KEY)
  const fastModeItems = () => optionGroupItems(props.availableOptionGroups, FAST_MODE_KEY)
  const thinkingItems = () => optionGroupItems(props.availableOptionGroups, ALWAYS_THINKING_KEY)

  // Identify the first visible group in each column so settingsFieldsetFirst
  // is applied only to it.
  const leftFirstGroup = () => thinkingItems().length > 0 ? 'thinking' : 'effort'
  const rightFirstGroup = () => fastModeItems().length > 0 ? 'fast' : outputStyleItems().length > 0 ? 'output' : 'mode'
  const firstLeftClass = (id: string) => leftFirstGroup() === id ? styles.settingsFieldsetFirst : undefined
  const firstRightClass = (id: string) => rightFirstGroup() === id ? styles.settingsFieldsetFirst : undefined

  return (
    <div class={styles.settingsPanelColumns}>
      <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnPrimary].join(' ')}>
        <Show when={thinkingItems().length > 0}>
          <RadioGroup
            label={optionGroup(props.availableOptionGroups, ALWAYS_THINKING_KEY)?.label || 'Extended Thinking'}
            items={thinkingItems()}
            testIdPrefix="thinking"
            name={`${menuId}-thinking`}
            current={currentThinking()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: ALWAYS_THINKING_KEY, value: v })}
            fieldsetClass={firstLeftClass('thinking')}
          />
        </Show>
        <Show when={props.availableModels && props.availableModels.length > 0}>
          <Show when={hasEffort()}>
            <RadioGroup
              label="Effort"
              items={efforts()}
              testIdPrefix="effort"
              name={`${menuId}-effort`}
              current={currentEffort()}
              onChange={v => props.onChange?.({ kind: 'effort', value: v })}
              fieldsetClass={firstLeftClass('effort')}
            />
          </Show>
          <ModelSelect
            items={models()}
            testIdPrefix="model"
            name={`${menuId}-model`}
            current={currentModel()}
            onChange={v => props.onChange?.({ kind: 'model', value: v })}
          />
        </Show>
      </div>
      <div class={styles.settingsPanelColumn}>
        <Show when={fastModeItems().length > 0}>
          <RadioGroup
            label={optionGroup(props.availableOptionGroups, FAST_MODE_KEY)?.label || 'Fast Mode'}
            items={fastModeItems()}
            testIdPrefix="fast-mode"
            name={`${menuId}-fast-mode`}
            current={currentFastMode()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: FAST_MODE_KEY, value: v })}
            fieldsetClass={firstRightClass('fast')}
          />
        </Show>
        <Show when={outputStyleItems().length > 0}>
          <RadioGroup
            label={optionGroup(props.availableOptionGroups, OUTPUT_STYLE_KEY)?.label || 'Output Style'}
            items={outputStyleItems()}
            testIdPrefix="output-style"
            name={`${menuId}-output-style`}
            current={currentOutputStyle()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: OUTPUT_STYLE_KEY, value: v })}
            fieldsetClass={firstRightClass('output')}
          />
        </Show>
        <RadioGroup
          label={modeGroup()?.label || 'Permission Mode'}
          items={modeItems()}
          testIdPrefix="permission-mode"
          name={`${menuId}-mode`}
          current={currentMode()}
          onChange={v => props.onChange?.({ kind: 'permissionMode', value: v })}
          fieldsetClass={firstRightClass('mode')}
        />
      </div>
    </div>
  )
}

/** Claude Code trigger label (model, effort icon, permission mode). */
export function ClaudeCodeTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const currentModel = () => props.model || DEFAULT_CLAUDE_MODEL
  const currentEffort = () => props.effort || DEFAULT_CLAUDE_EFFORT
  const currentMode = () => props.permissionMode || 'default'

  const displayName = () => modelDisplayName(props.availableModels, currentModel())

  const hasEffort = () => hasEfforts(props.availableModels, currentModel())
  const mode = () => optionLabel(props.availableOptionGroups, PERMISSION_MODE_KEY, currentMode())

  return (
    <>
      <Show when={props.availableModels && props.availableModels.length > 0}>
        {displayName()}
        <Show when={hasEffort()}>{effortIcon(currentEffort(), CLAUDE_EFFORT_ICONS)}</Show>
      </Show>
      {mode()}
    </>
  )
}

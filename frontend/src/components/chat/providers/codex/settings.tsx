import type { JSX } from 'solid-js'
import type { ProviderSettingsPanelProps } from '../registry'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import Sparkles from 'lucide-solid/icons/sparkles'
import Zap from 'lucide-solid/icons/zap'
import { createUniqueId, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { EFFORT_AUTO } from '~/utils/controlResponse'
import * as styles from '../../ChatView.css'
import { defaultModelId, effortItems, hasEfforts, modelDisplayName, modelItems, ModelSelect, optionGroup, optionGroupItems, optionLabel, PERMISSION_MODE_KEY, permissionModeGroup, permissionModeItems, RadioGroup } from '../../settingsShared'

/** Default model for Codex agents. */
const DEFAULT_CODEX_MODEL = import.meta.env.LEAPMUX_CODEX_DEFAULT_MODEL || 'gpt-5.4'
const DEFAULT_CODEX_EFFORT = EFFORT_AUTO
export const DEFAULT_CODEX_COLLABORATION_MODE = 'default'
export const DEFAULT_CODEX_SANDBOX_POLICY = 'workspace-write'
export const DEFAULT_CODEX_NETWORK_ACCESS = 'restricted'
export const DEFAULT_CODEX_SERVICE_TIER = 'default'
export const CODEX_EXTRA_COLLABORATION_MODE = 'collaboration_mode'
export const CODEX_EXTRA_SANDBOX_POLICY = 'sandbox_policy'
export const CODEX_EXTRA_NETWORK_ACCESS = 'network_access'
export const CODEX_EXTRA_SERVICE_TIER = 'service_tier'

export { DEFAULT_CODEX_EFFORT, DEFAULT_CODEX_MODEL }

/** Codex settings panel (model, effort, approval policy, sandbox). */
export function CodexSettingsPanel(props: ProviderSettingsPanelProps): JSX.Element {
  const menuId = createUniqueId()
  const extra = () => props.extraSettings || {}
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentCollaborationMode = () => extra()[CODEX_EXTRA_COLLABORATION_MODE] || DEFAULT_CODEX_COLLABORATION_MODE
  const currentSandbox = () => extra()[CODEX_EXTRA_SANDBOX_POLICY] || DEFAULT_CODEX_SANDBOX_POLICY
  const currentNetwork = () => extra()[CODEX_EXTRA_NETWORK_ACCESS] || DEFAULT_CODEX_NETWORK_ACCESS
  const currentServiceTier = () => extra()[CODEX_EXTRA_SERVICE_TIER] || DEFAULT_CODEX_SERVICE_TIER

  const models = () => modelItems(props.availableModels)
  const efforts = () => effortItems(props.availableModels, currentModel())
  const serviceTierGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_SERVICE_TIER)
  const serviceTierItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_SERVICE_TIER)
  const collaborationModeGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_COLLABORATION_MODE)
  const collaborationModeItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_COLLABORATION_MODE)
  const modeGroup = () => permissionModeGroup(props.availableOptionGroups)
  const modeItems = () => permissionModeItems(props.availableOptionGroups)
  const sandboxGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_SANDBOX_POLICY)
  const sandboxItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_SANDBOX_POLICY)
  const networkGroup = () => optionGroup(props.availableOptionGroups, CODEX_EXTRA_NETWORK_ACCESS)
  const networkItems = () => optionGroupItems(props.availableOptionGroups, CODEX_EXTRA_NETWORK_ACCESS)

  // Identify the first visible group in each column so settingsFieldsetFirst
  // is applied only to it. Using a derived signal avoids fragile cascading
  // conditionals that must be updated every time a new group is added.
  const leftFirstGroup = () => serviceTierItems().length > 0 ? 'tier' : 'effort'
  const rightFirstGroup = () =>
    collaborationModeItems().length > 0
      ? 'collab'
      : networkItems().length > 0
        ? 'network'
        : sandboxItems().length > 0
          ? 'sandbox'
          : 'mode'
  const firstLeftClass = (id: string) => leftFirstGroup() === id ? styles.settingsFieldsetFirst : undefined
  const firstRightClass = (id: string) => rightFirstGroup() === id ? styles.settingsFieldsetFirst : undefined

  return (
    <div class={styles.settingsPanelColumns}>
      <div class={[styles.settingsPanelColumn, styles.settingsPanelColumnPrimary].join(' ')}>
        <Show when={serviceTierItems().length > 0}>
          <RadioGroup
            label={serviceTierGroup()?.label || 'Fast Mode'}
            items={serviceTierItems()}
            testIdPrefix="codex-service-tier"
            name={`${menuId}-service-tier`}
            current={currentServiceTier()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: CODEX_EXTRA_SERVICE_TIER, value: v })}
            fieldsetClass={firstLeftClass('tier')}
          />
        </Show>
        <RadioGroup
          label="Reasoning Effort"
          items={efforts()}
          testIdPrefix="effort"
          name={`${menuId}-effort`}
          current={currentEffort()}
          onChange={v => props.onChange?.({ kind: 'effort', value: v })}
          fieldsetClass={firstLeftClass('effort')}
        />
        <ModelSelect
          items={models()}
          testIdPrefix="model"
          name={`${menuId}-model`}
          current={currentModel()}
          onChange={v => props.onChange?.({ kind: 'model', value: v })}
        />
      </div>
      <div class={styles.settingsPanelColumn}>
        <Show when={collaborationModeItems().length > 0}>
          <RadioGroup
            label={collaborationModeGroup()?.label || 'Workflow'}
            items={collaborationModeItems()}
            testIdPrefix="codex-collaboration-mode"
            name={`${menuId}-collaboration-mode`}
            current={currentCollaborationMode()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: CODEX_EXTRA_COLLABORATION_MODE, value: v })}
            fieldsetClass={firstRightClass('collab')}
          />
        </Show>
        <Show when={networkItems().length > 0}>
          <RadioGroup
            label={networkGroup()?.label || 'Network Access'}
            items={networkItems()}
            testIdPrefix="network"
            name={`${menuId}-network`}
            current={currentNetwork()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: CODEX_EXTRA_NETWORK_ACCESS, value: v })}
            fieldsetClass={firstRightClass('network')}
          />
        </Show>
        <Show when={sandboxItems().length > 0}>
          <RadioGroup
            label={sandboxGroup()?.label || 'Sandbox'}
            items={sandboxItems()}
            testIdPrefix="sandbox"
            name={`${menuId}-sandbox`}
            current={currentSandbox()}
            onChange={v => props.onChange?.({ kind: 'optionGroup', key: CODEX_EXTRA_SANDBOX_POLICY, value: v })}
            fieldsetClass={firstRightClass('sandbox')}
          />
        </Show>
        <div>
          <RadioGroup
            label={modeGroup()?.label || 'Approval Policy'}
            items={modeItems()}
            testIdPrefix="permission-mode"
            name={`${menuId}-mode`}
            current={currentMode()}
            onChange={v => props.onChange?.({ kind: 'permissionMode', value: v })}
            fieldsetClass={firstRightClass('mode')}
          />
        </div>
        <button
          class="outline small"
          style={{ 'margin-bottom': 'var(--space-2)' }}
          data-testid="codex-bypass-permissions"
          disabled={currentNetwork() === 'enabled' && currentSandbox() === 'danger-full-access' && currentMode() === 'never'}
          onClick={() => {
            props.onChange?.({ kind: 'optionGroup', key: CODEX_EXTRA_NETWORK_ACCESS, value: 'enabled' })
            props.onChange?.({ kind: 'optionGroup', key: CODEX_EXTRA_SANDBOX_POLICY, value: 'danger-full-access' })
            props.onChange?.({ kind: 'permissionMode', value: 'never' })
          }}
        >
          Bypass permissions
        </button>
      </div>
    </div>
  )
}

/** Codex trigger label (model name, effort icon, current mode). */
export function CodexTriggerLabel(props: ProviderSettingsPanelProps): JSX.Element {
  const extra = () => props.extraSettings || {}
  const currentModel = () => props.model || defaultModelId(props.availableModels) || DEFAULT_CODEX_MODEL
  const currentEffort = () => props.effort || DEFAULT_CODEX_EFFORT
  const currentMode = () => props.permissionMode || 'on-request'
  const currentCollaborationMode = () => extra()[CODEX_EXTRA_COLLABORATION_MODE] || DEFAULT_CODEX_COLLABORATION_MODE
  const displayName = () => modelDisplayName(props.availableModels, currentModel())

  const effortIcon = () => {
    switch (currentEffort()) {
      case 'auto': return <Icon icon={Sparkles} size="xs" />
      case 'xhigh': return <Icon icon={Zap} size="xs" />
      case 'high': return <Icon icon={ChevronsUp} size="xs" />
      case 'low': return <Icon icon={ChevronsDown} size="xs" />
      case 'minimal': return <Icon icon={ChevronsDown} size="xs" />
      case 'none': return <Icon icon={ChevronsDown} size="xs" />
      default: return <Icon icon={Dot} size="xs" />
    }
  }

  const hasEffort = () => hasEfforts(props.availableModels, currentModel())
  const mode = () => currentCollaborationMode() === 'plan'
    ? optionLabel(props.availableOptionGroups, CODEX_EXTRA_COLLABORATION_MODE, currentCollaborationMode())
    : optionLabel(props.availableOptionGroups, PERMISSION_MODE_KEY, currentMode())
  return (
    <>
      {displayName()}
      <Show when={hasEffort()}>{effortIcon()}</Show>
      {' '}
      {mode()}
    </>
  )
}

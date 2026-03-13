import type { JSX } from 'solid-js'
import type { PermissionMode } from '~/utils/controlResponse'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import Zap from 'lucide-solid/icons/zap'
import { createUniqueId, For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { spinner } from '~/styles/animations.css'
import { DEFAULT_EFFORT, DEFAULT_MODEL, EFFORT_LABELS, MODEL_LABELS, PERMISSION_MODE_LABELS } from '~/utils/controlResponse'
import * as styles from './ChatView.css'

export const PERMISSION_MODES = Object.entries(PERMISSION_MODE_LABELS).map(([value, label]) => ({ label, value }))
export const MODELS = Object.entries(MODEL_LABELS).map(([value, label]) => ({ label, value }))
export const EFFORTS = Object.entries(EFFORT_LABELS).map(([value, label]) => ({ label, value }))

export function modeLabel(mode: string): string {
  return PERMISSION_MODE_LABELS[mode as keyof typeof PERMISSION_MODE_LABELS] ?? 'Default'
}

export function modelLabel(model: string): string {
  return MODELS.find(m => m.value === model)?.label ?? 'Sonnet'
}

export interface EditorSettingsDropdownProps {
  disabled?: boolean
  settingsLoading?: boolean
  model?: string
  effort?: string
  permissionMode?: string
  supportsModelEffort?: boolean
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onPermissionModeChange?: (mode: PermissionMode) => void
}

function RadioGroup(props: {
  label: string
  items: { label: string, value: string }[]
  testIdPrefix: string
  name: string
  current: string
  onChange: (value: string) => void
}): JSX.Element {
  return (
    <fieldset>
      <legend class={styles.settingsGroupLabel}>{props.label}</legend>
      <For each={props.items}>
        {item => (
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
        )}
      </For>
    </fieldset>
  )
}

export function EditorSettingsDropdown(props: EditorSettingsDropdownProps): JSX.Element {
  let settingsPopoverEl: HTMLElement | undefined
  const menuId = createUniqueId()

  const currentModel = () => props.model || DEFAULT_MODEL
  const currentEffort = () => props.effort || DEFAULT_EFFORT
  const currentMode = () => props.permissionMode || 'default'

  const effortIcon = () => {
    switch (currentEffort()) {
      case 'low': return <Icon icon={ChevronsDown} size="xs" />
      case 'high': return <Icon icon={ChevronsUp} size="xs" />
      case 'max': return <Icon icon={Zap} size="xs" />
      default: return <Icon icon={Dot} size="xs" />
    }
  }

  return (
    <DropdownMenu
      trigger={triggerProps => (
        <button
          class={styles.settingsTrigger}
          data-testid="agent-settings-trigger"
          disabled={props.disabled}
          {...triggerProps}
        >
          <Show when={props.supportsModelEffort !== false}>
            {modelLabel(currentModel())}
            {effortIcon()}
          </Show>
          {modeLabel(currentMode())}
          <Show when={props.settingsLoading} fallback={<Icon icon={ChevronDown} size="xs" />}>
            <Icon icon={LoaderCircle} size="xs" class={spinner} data-testid="settings-loading-spinner" />
          </Show>
        </button>
      )}
      popoverRef={(el) => { settingsPopoverEl = el }}
      class={styles.settingsMenu}
      data-testid="agent-settings-menu"
    >
      <Show when={props.supportsModelEffort !== false}>
        <RadioGroup
          label="Effort"
          items={EFFORTS}
          testIdPrefix="effort"
          name={`${menuId}-effort`}
          current={currentEffort()}
          onChange={(v) => {
            props.onEffortChange?.(v)
            settingsPopoverEl?.hidePopover()
          }}
        />
        <RadioGroup
          label="Model"
          items={MODELS}
          testIdPrefix="model"
          name={`${menuId}-model`}
          current={currentModel()}
          onChange={(v) => {
            props.onModelChange?.(v)
            settingsPopoverEl?.hidePopover()
          }}
        />
      </Show>
      <RadioGroup
        label="Permission Mode"
        items={PERMISSION_MODES}
        testIdPrefix="permission-mode"
        name={`${menuId}-mode`}
        current={currentMode()}
        onChange={(v) => {
          props.onPermissionModeChange?.(v as PermissionMode)
          settingsPopoverEl?.hidePopover()
        }}
      />
    </DropdownMenu>
  )
}

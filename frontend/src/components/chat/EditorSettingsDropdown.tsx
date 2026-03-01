import type { JSX } from 'solid-js'
import type { PermissionMode } from '~/utils/controlResponse'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronsDown from 'lucide-solid/icons/chevrons-down'
import ChevronsUp from 'lucide-solid/icons/chevrons-up'
import Dot from 'lucide-solid/icons/dot'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
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
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onPermissionModeChange?: (mode: PermissionMode) => void
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
          {modelLabel(currentModel())}
          {effortIcon()}
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
      {/* Effort */}
      <fieldset>
        <legend class={styles.settingsGroupLabel}>Effort</legend>
        <For each={EFFORTS}>
          {effort => (
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`effort-${effort.value}`}
            >
              <input
                type="radio"
                name={`${menuId}-effort`}
                value={effort.value}
                checked={currentEffort() === effort.value}
                onChange={() => {
                  props.onEffortChange?.(effort.value)
                  settingsPopoverEl?.hidePopover()
                }}
              />
              {effort.label}
            </label>
          )}
        </For>
      </fieldset>

      {/* Model */}
      <fieldset>
        <legend class={styles.settingsGroupLabel}>Model</legend>
        <For each={MODELS}>
          {model => (
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`model-${model.value}`}
            >
              <input
                type="radio"
                name={`${menuId}-model`}
                value={model.value}
                checked={currentModel() === model.value}
                onChange={() => {
                  props.onModelChange?.(model.value)
                  settingsPopoverEl?.hidePopover()
                }}
              />
              {model.label}
            </label>
          )}
        </For>
      </fieldset>

      {/* Permission Mode */}
      <fieldset>
        <legend class={styles.settingsGroupLabel}>Permission Mode</legend>
        <For each={PERMISSION_MODES}>
          {mode => (
            <label
              role="menuitemradio"
              class={styles.settingsRadioItem}
              data-testid={`permission-mode-${mode.value}`}
            >
              <input
                type="radio"
                name={`${menuId}-mode`}
                value={mode.value}
                checked={currentMode() === mode.value}
                onChange={() => {
                  props.onPermissionModeChange?.(mode.value as PermissionMode)
                  settingsPopoverEl?.hidePopover()
                }}
              />
              {mode.label}
            </label>
          )}
        </For>
      </fieldset>
    </DropdownMenu>
  )
}

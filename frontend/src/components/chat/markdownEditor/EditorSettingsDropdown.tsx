import type { JSX } from 'solid-js'
import type { ProviderSettingChange, ProviderSettingsPanelProps, ProviderSettingsState } from '../providers/registry'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { spinner } from '~/styles/animations.css'
import * as styles from '../ChatView.css'
import { providerFor } from '../providers/registry'
import '../providers'

export interface EditorSettingsDropdownProps extends ProviderSettingsState {
  agentProvider?: AgentProvider
  onChange?: (change: ProviderSettingChange) => void
}

export function EditorSettingsDropdown(props: EditorSettingsDropdownProps): JSX.Element {
  const provider = createMemo(() => props.agentProvider ?? AgentProvider.CLAUDE_CODE)
  const plugin = createMemo(() => providerFor(provider()))

  const settingsPanelProps = (): ProviderSettingsPanelProps => ({
    disabled: props.disabled,
    settingsLoading: props.settingsLoading,
    model: props.model,
    effort: props.effort,
    permissionMode: props.permissionMode,
    extraSettings: props.extraSettings,
    availableModels: props.availableModels,
    availableOptionGroups: props.availableOptionGroups,
    onChange: props.onChange,
  })

  return (
    <DropdownMenu
      trigger={triggerProps => (
        <button
          class={styles.settingsTrigger}
          data-testid="agent-settings-trigger"
          disabled={props.disabled}
          {...triggerProps}
        >
          {plugin()?.settingsTriggerLabel?.(settingsPanelProps())}
          <Show when={props.settingsLoading} fallback={<Icon icon={ChevronDown} size="xs" />}>
            <Icon icon={LoaderCircle} size="xs" class={spinner} data-testid="settings-loading-spinner" />
          </Show>
        </button>
      )}
      class={[styles.settingsMenu, plugin()?.settingsMenuClass].filter(Boolean).join(' ')}
      data-testid="agent-settings-menu"
    >
      {plugin()?.SettingsPanel && <Dynamic component={plugin()!.SettingsPanel!} {...settingsPanelProps()} />}
    </DropdownMenu>
  )
}

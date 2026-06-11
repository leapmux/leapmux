import type { JSX } from 'solid-js'
import type { ProviderSettingChange, ProviderSettingsPanelProps, ProviderSettingsState } from '../providers/registry'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Spinner } from '~/components/common/Spinner'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { AgentSettingsPanel, AgentSettingsPanelTriggerLabel } from '../AgentSettingsPanel'
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
    optionValues: props.optionValues,
    optionGroups: props.optionGroups,
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
          <AgentSettingsPanelTriggerLabel {...settingsPanelProps()} triggerModeGroupKey={plugin()?.triggerModeGroupKey} />
          <Show when={props.settingsLoading} fallback={<Icon icon={ChevronDown} size="xs" />}>
            <Spinner size="xs" data-testid="settings-loading-spinner" />
          </Show>
        </button>
      )}
      class={styles.settingsMenu}
      data-testid="agent-settings-menu"
    >
      <AgentSettingsPanel {...settingsPanelProps()} actions={plugin()?.settingsActions} />
    </DropdownMenu>
  )
}

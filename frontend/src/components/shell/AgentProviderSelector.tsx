import type { Accessor } from 'solid-js'
import Check from 'lucide-solid/icons/check'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, For, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { RefreshButton } from '~/components/common/RefreshButton'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getAvailableAgentProviders, sortAgentProvidersByName } from '~/lib/agentProviders'
import { labelRow } from '~/styles/shared.css'
import * as styles from './AgentProviderSelector.css'

interface AgentProviderSelectorProps {
  value: Accessor<AgentProvider>
  onChange: (provider: AgentProvider) => void
  availableProviders?: AgentProvider[]
  onRefresh?: () => void
}

export function AgentProviderSelector(props: AgentProviderSelectorProps) {
  const providers = createMemo(() => sortAgentProvidersByName(getAvailableAgentProviders(props.availableProviders)))
  const currentProvider = createMemo(() => {
    const current = props.value()
    return providers().includes(current) ? current : providers()[0] ?? AgentProvider.CLAUDE_CODE
  })

  return (
    <div>
      <div class={labelRow}>
        Agent Provider
        {props.onRefresh && (
          <RefreshButton onClick={() => props.onRefresh?.()} title="Refresh available providers" />
        )}
      </div>
      <DropdownMenu
        class={styles.menu}
        data-testid="agent-provider-selector-menu"
        trigger={triggerProps => (
          <button
            type="button"
            aria-expanded={triggerProps['aria-expanded']}
            ref={triggerProps.ref}
            onPointerDown={triggerProps.onPointerDown}
            onClick={triggerProps.onClick}
            class={styles.trigger}
            data-testid="agent-provider-selector-trigger"
          >
            <span class={styles.triggerValue}>
              <AgentProviderIcon provider={currentProvider()} size={16} />
              <span class={styles.triggerLabel}>{agentProviderLabel(currentProvider())}</span>
            </span>
            <ChevronDown size={16} class={styles.triggerChevron} />
          </button>
        )}
      >
        <For each={providers()}>
          {provider => (
            <button
              type="button"
              role="menuitem"
              class={`${styles.menuItem}${provider === currentProvider() ? ` ${styles.menuItemSelected}` : ''}`}
              data-testid={`agent-provider-option-${provider}`}
              onClick={() => props.onChange(provider)}
            >
              <span class={styles.menuItemValue}>
                <AgentProviderIcon provider={provider} size={16} />
                <span>{agentProviderLabel(provider)}</span>
              </span>
              <Show when={provider === currentProvider()}>
                <Check size={14} class={styles.check} />
              </Show>
            </button>
          )}
        </For>
      </DropdownMenu>
    </div>
  )
}

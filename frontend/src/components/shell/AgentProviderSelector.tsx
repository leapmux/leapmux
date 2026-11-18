import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, For, Show } from 'solid-js'
import { AgentProviderIcon, agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { ClippedText } from '~/components/common/ClippedText'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'
import { sortAgentProvidersByName } from '~/lib/agentProviders'
import { clippedText, fieldTriggerChevron } from '~/styles/shared.css'
import * as styles from './AgentProviderSelector.css'

interface AgentProviderSelectorProps {
  value: Accessor<AgentProvider | undefined>
  onChange: (provider: AgentProvider) => void
  availableProviders?: readonly AgentProvider[] | undefined
  loading?: boolean
  onRefresh?: () => void
}

export function AgentProviderSelector(props: AgentProviderSelectorProps) {
  const providers = createMemo(() => sortAgentProvidersByName(props.availableProviders ?? []))
  const currentProvider = createMemo(() => {
    const current = props.value()
    return current !== undefined && providers().includes(current)
      ? current
      : undefined
  })
  const emptyLabel = () => {
    if (props.loading)
      return 'Checking available agents…'
    return props.availableProviders === undefined
      ? 'Agent availability unknown'
      : 'No agents available'
  }

  return (
    <LabeledField
      label="Agent Provider"
      actions={props.onRefresh && (
        <RefreshButton onClick={() => props.onRefresh?.()} disabled={props.loading ?? false} title="Refresh available providers" />
      )}
    >
      <Show
        when={currentProvider()}
        fallback={(
          <button
            type="button"
            class={`${styles.trigger} ${styles.triggerDisabled}`}
            disabled
            data-testid="agent-provider-selector-trigger"
          >
            <span class={styles.triggerValue}>
              <Icon icon={Bot} size="sm" />
              <span class={clippedText}>{emptyLabel()}</span>
            </span>
          </button>
        )}
      >
        {selection => (
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
                  <AgentProviderIcon provider={selection()} size={16} />
                  <ClippedText text={agentProviderLabel(selection())} />
                </span>
                <ChevronDown size={16} class={fieldTriggerChevron} />
              </button>
            )}
          >
            <For each={providers()}>
              {provider => (
                <button
                  type="button"
                  role="menuitem"
                  class={`${styles.menuItem}${provider === currentProvider()
                    ? ` ${styles.menuItemSelected}`
                    : ''}`}
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
        )}
      </Show>
    </LabeledField>
  )
}

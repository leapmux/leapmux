import type { Component } from 'solid-js'
import type { IconSizeName } from '~/components/common/Icon'
import type { Tab } from '~/stores/tab.types'
import FileText from 'lucide-solid/icons/file-text'
import Terminal from 'lucide-solid/icons/terminal'
import { Match, Switch } from 'solid-js'
import { AgentProviderIcon } from '~/components/common/AgentProviderIcon'
import { Icon } from '~/components/common/Icon'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { isAgentTab } from '~/stores/tab.types'
import { iconSize } from '~/styles/tokens'

export interface TabTypeIconProps {
  tab: Tab
  size?: IconSizeName
  class?: string
}

// Shared per-tab-type icon. Used by TabBar (tab strip) and
// WorkspaceTabTree (sidebar tree) so the two surfaces always agree on
// which icon represents which tab type.
export const TabTypeIcon: Component<TabTypeIconProps> = (props) => {
  const tokenSize = (): IconSizeName => props.size ?? 'sm'
  return (
    <Switch>
      <Match when={isAgentTab(props.tab) ? props.tab : false}>
        {tab => (
          <AgentProviderIcon
            provider={tab().agentProvider}
            size={iconSize[tokenSize()]}
            class={props.class}
          />
        )}
      </Match>
      <Match when={props.tab.type === TabType.FILE}>
        <Icon icon={FileText} size={tokenSize()} class={props.class} />
      </Match>
      <Match when={props.tab.type === TabType.TERMINAL}>
        <Icon icon={Terminal} size={tokenSize()} class={props.class} />
      </Match>
    </Switch>
  )
}

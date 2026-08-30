import type { Component } from 'solid-js'
import type { IconSizeName } from '~/components/common/Icon'
import type { Tab } from '~/stores/tab.types'
import CornerDownRight from 'lucide-solid/icons/corner-down-right'
import FileText from 'lucide-solid/icons/file-text'
import Terminal from 'lucide-solid/icons/terminal'
import { Match, Show, Switch } from 'solid-js'
import { AgentProviderIcon } from '~/components/common/AgentProviderIcon'
import { Icon } from '~/components/common/Icon'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { isAgentTab } from '~/stores/tab.types'
import { iconSize } from '~/styles/tokens'
import * as styles from './TabTypeIcon.css'

export interface TabTypeIconProps {
  tab: Tab
  size?: IconSizeName
  class?: string
}

// Shared per-tab-type icon. Used by TabBar (tab strip) and
// WorkspaceTabTree (sidebar tree) so the two surfaces always agree on
// which icon represents which tab type. A subagent (child) tab wraps the
// provider icon in a relative span with a small corner overlay glyph so it is
// distinguishable from its root parent at a glance.
export const TabTypeIcon: Component<TabTypeIconProps> = (props) => {
  const tokenSize = (): IconSizeName => props.size ?? 'sm'
  return (
    <Switch>
      <Match when={isAgentTab(props.tab) ? props.tab : false}>
        {tab => (
          <span class={styles.wrapper}>
            <AgentProviderIcon
              provider={tab().agentProvider}
              size={iconSize[tokenSize()]}
              class={props.class}
            />
            <Show when={tab().parentAgentId}>
              <span class={styles.subagentOverlay}>
                <CornerDownRight size={Math.round(iconSize[tokenSize()] * 0.6)} />
              </span>
            </Show>
          </span>
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

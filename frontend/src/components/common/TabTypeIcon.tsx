import type { Component } from 'solid-js'
import type { IconSizeName } from '~/components/common/Icon'
import type { Tab } from '~/stores/tab.types'
import CornerDownRight from 'lucide-solid/icons/corner-down-right'
import FileText from 'lucide-solid/icons/file-text'
import ImageIcon from 'lucide-solid/icons/image'
import Terminal from 'lucide-solid/icons/terminal'
import { Match, Show, Switch } from 'solid-js'
import { AgentProviderIcon } from '~/components/common/AgentProviderIcon'
import { Icon } from '~/components/common/Icon'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { isSubagentTab } from '~/stores/tab.helpers'
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
  // Spread as a getter so the class stays ABSENT when unset, and the read
  // stays reactive, rather than freezing the decision at mount.
  const classProps = () => (props.class !== undefined ? { class: props.class } : {})
  return (
    <Switch>
      <Match when={isAgentTab(props.tab) ? props.tab : false}>
        {(tab) => {
          // Spread-ready provider, as a GETTER like `classProps` below: the
          // row updates in place (the tree's fingerprint skips provider
          // changes), so the read must stay in the spread's reactive scope.
          // One read per evaluation keeps presence and value decided
          // together -- a second `tab().agentProvider` beside the first
          // could not be narrowed.
          const providerProps = () => {
            const provider = tab().agentProvider
            return provider !== undefined ? { provider } : {}
          }
          return (
            <span class={styles.wrapper}>
              <AgentProviderIcon
                {...providerProps()}
                size={iconSize[tokenSize()]}
                {...classProps()}
              />
              <Show when={isSubagentTab(tab())}>
                <span class={styles.subagentOverlay}>
                  <CornerDownRight size={Math.round(iconSize[tokenSize()] * 0.6)} />
                </span>
              </Show>
            </span>
          )
        }}
      </Match>
      <Match when={props.tab.type === TabType.FILE}>
        <Icon icon={FileText} size={tokenSize()} {...classProps()} />
      </Match>
      <Match when={props.tab.type === TabType.IMAGE}>
        <Icon icon={ImageIcon} size={tokenSize()} {...classProps()} />
      </Match>
      <Match when={props.tab.type === TabType.TERMINAL}>
        <Icon icon={Terminal} size={tokenSize()} {...classProps()} />
      </Match>
    </Switch>
  )
}

import type { Component } from 'solid-js'
import { Match, Show, Switch } from 'solid-js'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import * as startupPanelStyles from '~/components/common/StartupPanel.css'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'

export interface AgentStartupBannerProps {
  status: AgentStatus | undefined
  providerLabel: string | undefined
  startupError: string | undefined
  startupMessage: string | undefined
  onOpenStartupPrompt?: (() => void) | undefined
  containerClass: string
}

export const AgentStartupBanner: Component<AgentStartupBannerProps> = props => (
  <Switch>
    <Match when={props.status === AgentStatus.STARTING}>
      <div class={props.containerClass} data-testid="agent-startup-overlay">
        <Show
          when={props.onOpenStartupPrompt}
          fallback={<StartupSpinner label={props.startupMessage || `Starting ${props.providerLabel ?? 'agent'}…`} />}
        >
          {onOpen => (
            <div class={startupPanelStyles.startupBody}>
              <div class={startupPanelStyles.startupActions}>
                <StartupSpinner label={props.startupMessage || `Starting ${props.providerLabel ?? 'agent'}…`} />
              </div>
              <div class={startupPanelStyles.startupActions}>
                <button
                  type="button"
                  class="outline"
                  data-testid="agent-startup-prompt-open"
                  onClick={() => onOpen()()}
                >
                  Review instructions
                </button>
              </div>
            </div>
          )}
        </Show>
      </div>
    </Match>
    <Match when={props.status === AgentStatus.STARTUP_FAILED}>
      <div
        class={props.containerClass}
        data-testid="agent-startup-error"
        style={{ color: 'var(--danger)' }}
      >
        <StartupErrorBody
          title={`${props.providerLabel ?? 'Agent'} failed to start`}
          error={props.startupError ?? ''}
        />
      </div>
    </Match>
  </Switch>
)

import type { Component } from 'solid-js'
import type { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { Match, Switch } from 'solid-js'
import { StartupErrorBody, StartupSpinner } from '~/components/common/StartupPanel'
import { AgentStatus as AgentStatusEnum } from '~/generated/leapmux/v1/agent_pb'

export interface AgentStartupBannerProps {
  status: AgentStatus | undefined
  providerLabel: string | undefined
  startupError: string | undefined
  startupMessage: string | undefined
  containerClass: string
}

export const AgentStartupBanner: Component<AgentStartupBannerProps> = props => (
  <Switch>
    <Match when={props.status === AgentStatusEnum.STARTING}>
      <div class={props.containerClass} data-testid="agent-startup-overlay">
        <StartupSpinner label={props.startupMessage || `Starting ${props.providerLabel ?? 'agent'}…`} />
      </div>
    </Match>
    <Match when={props.status === AgentStatusEnum.STARTUP_FAILED}>
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

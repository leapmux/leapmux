import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'

import { Match, Switch } from 'solid-js'
import { getToolName } from '~/utils/controlResponse'
import { ExitPlanModeActions, ExitPlanModeContent } from '../../controls/ExitPlanModeControl'
import { GenericToolActions, GenericToolContent } from '../../controls/GenericToolControl'

/** Claude Code control request content dispatcher. */
export const ClaudeCodeControlContent: Component<ContentProps> = (props) => {
  const toolName = () => props.request?.payload ? getToolName(props.request.payload) : ''
  return (
    <Switch fallback={<GenericToolContent request={props.request} />}>
      <Match when={toolName() === 'ExitPlanMode'}>
        <ExitPlanModeContent request={props.request} />
      </Match>
    </Switch>
  )
}

/** Claude Code control request actions dispatcher. */
export const ClaudeCodeControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => props.request?.payload ? getToolName(props.request.payload) : ''
  return (
    <Switch fallback={<GenericToolActions {...props} />}>
      <Match when={toolName() === 'ExitPlanMode'}>
        <ExitPlanModeActions {...props} />
      </Match>
    </Switch>
  )
}

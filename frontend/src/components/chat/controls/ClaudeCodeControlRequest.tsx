import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './types'

import { Match, Switch } from 'solid-js'
import { getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './AskUserQuestionControl'
import { ExitPlanModeActions, ExitPlanModeContent } from './ExitPlanModeControl'
import { GenericToolActions, GenericToolContent } from './GenericToolControl'

/** Claude Code control request content dispatcher. */
export const ClaudeCodeControlContent: Component<ContentProps> = (props) => {
  const toolName = () => props.request?.payload ? getToolName(props.request.payload) : ''
  return (
    <Switch fallback={<GenericToolContent request={props.request} />}>
      <Match when={toolName() === 'ExitPlanMode'}>
        <ExitPlanModeContent request={props.request} />
      </Match>
      <Match when={toolName() === 'AskUserQuestion' || toolName() === 'request_user_input'}>
        <AskUserQuestionContent request={props.request} askState={props.askState} optionsDisabled={props.optionsDisabled} />
      </Match>
    </Switch>
  )
}

/** Claude Code control request actions dispatcher. */
export const ClaudeCodeControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => props.request?.payload ? getToolName(props.request.payload) : ''
  return (
    <Switch fallback={(
      <div class={styles.controlFooter}>
        <div class={styles.controlFooterLeft}>
          {props.infoTrigger}
        </div>
        <div class={styles.controlFooterRight}>
          <GenericToolActions {...props} />
        </div>
      </div>
    )}
    >
      <Match when={toolName() === 'ExitPlanMode'}>
        <ExitPlanModeActions {...props} />
      </Match>
      <Match when={toolName() === 'AskUserQuestion' || toolName() === 'request_user_input'}>
        <AskUserQuestionActions {...props} />
      </Match>
    </Switch>
  )
}

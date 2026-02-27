import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './controls/types'
import { Match, Switch } from 'solid-js'
import { getToolName } from '~/utils/controlResponse'
import * as styles from './ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './controls/AskUserQuestionControl'
import { ExitPlanModeActions, ExitPlanModeContent } from './controls/ExitPlanModeControl'
import { GenericToolActions, GenericToolContent } from './controls/GenericToolControl'

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<ContentProps> = (props) => {
  const toolName = () => props.request?.payload ? getToolName(props.request.payload) : ''
  return (
    <div class={styles.controlBanner} data-testid="control-banner">
      <Switch fallback={<GenericToolContent request={props.request} />}>
        <Match when={toolName() === 'ExitPlanMode'}>
          <ExitPlanModeContent request={props.request} />
        </Match>
        <Match when={toolName() === 'AskUserQuestion' || toolName() === 'request_user_input'}>
          <AskUserQuestionContent request={props.request} askState={props.askState} optionsDisabled={props.optionsDisabled} />
        </Match>
      </Switch>
    </div>
  )
}

/** Renders control request action buttons only, for the footer slot. */
export const ControlRequestActions: Component<ActionsProps> = (props) => {
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

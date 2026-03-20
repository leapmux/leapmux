import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './controls/types'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import { createSignal, Match, Switch } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { getToolName } from '~/utils/controlResponse'
import * as styles from './ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './controls/AskUserQuestionControl'
import { ExitPlanModeActions, ExitPlanModeContent } from './controls/ExitPlanModeControl'
import { GenericToolActions, GenericToolContent } from './controls/GenericToolControl'
import { getProviderPlugin } from './providers/registry'
import { toolHeaderActions } from './toolStyles.css'

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<ContentProps> = (props) => {
  const toolName = () => props.request?.payload ? getToolName(props.request.payload) : ''
  const pluginContent = () => props.agentProvider != null ? getProviderPlugin(props.agentProvider)?.ControlContent : undefined
  const [copied, setCopied] = createSignal(false)

  const handleCopyJson = () => {
    const json = JSON.stringify(props.request.payload, null, 2)
    navigator.clipboard.writeText(json)
    setCopied(true)
    setTimeout(setCopied, 2000, false)
  }

  return (
    <div class={styles.controlBanner} data-testid="control-banner">
      <div class={`${styles.controlBannerActions} ${toolHeaderActions}`} data-testid="control-banner-actions">
        <IconButton
          icon={copied() ? Check : Braces}
          size="sm"
          onClick={handleCopyJson}
          title={copied() ? 'Copied' : 'Copy Raw JSON'}
          data-testid="control-copy-json"
        />
      </div>
      <Switch fallback={
        pluginContent()
          ? <Dynamic component={pluginContent()!} {...props} />
          : <GenericToolContent request={props.request} />
      }
      >
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
  const pluginActions = () => props.agentProvider != null ? getProviderPlugin(props.agentProvider)?.ControlActions : undefined
  return (
    <Switch fallback={(
      <div class={styles.controlFooter}>
        <div class={styles.controlFooterLeft}>
          {props.infoTrigger}
        </div>
        <div class={styles.controlFooterRight}>
          {pluginActions()
            ? <Dynamic component={pluginActions()!} {...props} />
            : <GenericToolActions {...props} />}
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

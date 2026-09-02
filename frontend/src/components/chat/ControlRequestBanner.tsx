import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './controls/types'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import * as styles from './ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './controls/AskUserQuestionControl'
import { pluginFor } from './providers/registry'

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<ContentProps> = (props) => {
  const plugin = () => pluginFor(props.agentProvider)
  const pluginContent = () => plugin()?.ControlContent
  const question = createMemo(() => {
    const capability = plugin()?.askUserQuestion
    return capability?.isRequest(props.request.payload)
      ? { capability, questions: capability.extractQuestions(props.request.payload) }
      : undefined
  })
  const { copied, copy } = useCopyButton(() => prettifyJson(props.request?.payload))

  return (
    <Show when={props.request}>
      <div class={styles.controlBanner} data-testid="control-banner">
        <div class={styles.controlBannerActions} data-testid="control-banner-actions">
          <IconButton
            icon={copied() ? Check : Braces}
            size="sm"
            onClick={copy}
            title={copied() ? 'Copied' : 'Copy Raw JSON'}
            data-testid="control-copy-json"
          />
        </div>
        <Show when={question()} fallback={<Dynamic component={pluginContent()} {...props} />}>
          {question => (
            <AskUserQuestionContent
              request={props.request}
              askState={props.askState}
              optionsDisabled={props.optionsDisabled}
              agentProvider={props.agentProvider}
              questions={question().questions}
            />
          )}
        </Show>
      </div>
    </Show>
  )
}

/** Renders control request action buttons only, for the footer slot. */
export const ControlRequestActions: Component<ActionsProps> = (props) => {
  const plugin = () => pluginFor(props.agentProvider)
  const pluginActions = () => plugin()?.ControlActions
  const question = createMemo(() => {
    const capability = plugin()?.askUserQuestion
    return capability?.isRequest(props.request.payload)
      ? { capability, questions: capability.extractQuestions(props.request.payload) }
      : undefined
  })
  return (
    <Show when={props.request}>
      <Show when={question()} fallback={<Dynamic component={pluginActions()} {...props} />}>
        {question => (
          <AskUserQuestionActions
            {...props}
            questions={question().questions}
            onSubmitAnswers={() => question().capability.sendAnswer(
              props.request.agentId,
              props.onRespond,
              props.request.requestId,
              question().questions,
              props.askState,
              props.request.payload,
            )}
            onReject={message => question().capability.sendReject(
              props.request.agentId,
              props.onRespond,
              props.request.requestId,
              message,
              props.request.payload,
            )}
          />
        )}
      </Show>
    </Show>
  )
}

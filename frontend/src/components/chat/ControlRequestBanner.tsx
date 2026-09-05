import type { Component } from 'solid-js'
import type { BannerActionsProps, BannerContentProps } from './controls/types'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import { createMemo, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import * as styles from './ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent, controlQuestion } from './controls/AskUserQuestionControl'
import { pluginFor } from './providers/registry'

function createControlQuestion(props: Pick<BannerContentProps, 'request' | 'agentProvider'>) {
  // This memo is a SIBLING of the `<Show when={props.request}>` below, never a
  // descendant, so that Show cannot dispose it first. A caller that passes
  // `request` as a reactive prop therefore re-runs this memo with the removed
  // request. `controlQuestion` accepts an absent request for that reason.
  // `AgentEditorPanel` keys its owner on the request and never does that, but
  // the prop is public.
  return createMemo(() => controlQuestion(props.request, props.agentProvider))
}

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<BannerContentProps> = (props) => {
  const plugin = () => pluginFor(props.agentProvider)
  const pluginContent = () => plugin()?.ControlContent
  const question = createControlQuestion(props)
  const { copied, copy } = useCopyButton(() => prettifyJson(props.request?.payload))

  return (
    <Show when={props.request}>
      {request => (
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
          <Show when={question()} fallback={<Dynamic component={pluginContent()} {...props} request={request()} />}>
            {question => (
              <AskUserQuestionContent
                {...props}
                request={request()}
                questions={question().questions}
              />
            )}
          </Show>
        </div>
      )}
    </Show>
  )
}

/** Renders control request action buttons only, for the footer slot. */
export const ControlRequestActions: Component<BannerActionsProps> = (props) => {
  const plugin = () => pluginFor(props.agentProvider)
  const pluginActions = () => plugin()?.ControlActions
  const question = createControlQuestion(props)
  return (
    <Show when={props.request}>
      {request => (
        <Show when={question()} fallback={<Dynamic component={pluginActions()} {...props} request={request()} />}>
          {question => (
            <AskUserQuestionActions
              {...props}
              request={request()}
              questions={question().questions}
              onSubmitAnswers={() => question().capability.sendAnswer(
                request(),
                props.onRespond,
                question().questions,
                props.answerState,
              )}
              onReject={message => question().capability.sendReject(request(), props.onRespond, message)}
            />
          )}
        </Show>
      )}
    </Show>
  )
}

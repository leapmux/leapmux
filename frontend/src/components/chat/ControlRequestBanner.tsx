import type { Component } from 'solid-js'
import type { BannerActionsProps, BannerContentProps } from './controls/types'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import { createMemo, onCleanup, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { useCopyButton } from '~/hooks/useCopyButton'
import { prettifyJson } from '~/lib/jsonFormat'
import { controlRequestProvider } from '~/stores/control.store'
import * as styles from './ControlRequestBanner.css'
import { useControlRequestSource } from './controlRequestSource'
import { AskUserQuestionActions, AskUserQuestionContent, controlQuestion } from './controls/AskUserQuestionControl'
import { ElicitationActions, ElicitationContent } from './controls/ElicitationControl'
import { pluginFor } from './providers/registry'

function createControlInputs(props: Pick<BannerContentProps, 'request' | 'agentProvider' | 'messageContext'>) {
  // This memo is a SIBLING of the `<Show when={props.request}>` below, never a
  // descendant, so that Show cannot dispose it first. A caller that passes
  // `request` as a reactive prop therefore re-runs this memo with the removed
  // request. `controlQuestion` accepts an absent request for that reason.
  // `AgentEditorPanel` keys its owner on the request and never does that, but
  // the prop is public.
  const provider = createMemo(() => controlRequestProvider(props.request, props.agentProvider))
  const source = useControlRequestSource(() => props.request, () => props.messageContext, provider)
  const question = createMemo(() => controlQuestion(props.request, provider(), source()))
  const elicitation = createMemo(() => props.request ? pluginFor(provider())?.elicitation?.(props.request.payload, source()) : undefined)
  return { question, elicitation, provider }
}

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<BannerContentProps> = (props) => {
  const { question, elicitation, provider } = createControlInputs(props)
  const plugin = () => pluginFor(provider())
  const pluginContent = () => plugin()?.ControlContent
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
          <Show when={question()} fallback={<Show when={elicitation()} fallback={<Dynamic component={pluginContent()} {...props} agentProvider={provider()} request={request()} />}>{elicitation => <ElicitationContent {...props} agentProvider={provider()} request={request()} elicitation={elicitation()} />}</Show>}>
            {question => (
              <AskUserQuestionContent
                {...props}
                agentProvider={provider()}
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
  const { question, elicitation, provider } = createControlInputs(props)
  const plugin = () => pluginFor(provider())
  const pluginActions = () => plugin()?.ControlActions
  return (
    <Show when={props.request}>
      {request => (
        <fieldset
          data-testid="control-actions"
          disabled={!props.answerState.ready()}
          aria-busy={!props.answerState.ready() ? 'true' : undefined}
          style={{ display: 'contents' }}
          ref={(element) => {
            const blockUntilReady = (event: MouseEvent) => {
              if (props.answerState.ready())
                return
              event.preventDefault()
              event.stopPropagation()
            }
            element.addEventListener('click', blockUntilReady, true)
            onCleanup(() => element.removeEventListener('click', blockUntilReady, true))
          }}
        >
          <Show when={question()} fallback={<Show when={elicitation()} fallback={<Dynamic component={pluginActions()} {...props} agentProvider={provider()} request={request()} />}>{elicitation => <ElicitationActions {...props} agentProvider={provider()} request={request()} elicitation={elicitation()} />}</Show>}>
            {question => (
              <AskUserQuestionActions
                {...props}
                agentProvider={provider()}
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
        </fieldset>
      )}
    </Show>
  )
}

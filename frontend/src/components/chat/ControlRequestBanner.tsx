import type { Component } from 'solid-js'
import type { BannerActionsProps, BannerContentProps } from './controls/types'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import Square from 'lucide-solid/icons/square'
import { createMemo, onCleanup, Show } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { prettifyJson } from '~/lib/jsonFormat'
import { controlRequestProvider } from '~/stores/control.store'
import * as styles from './ControlRequestBanner.css'
import { useControlRequestSource } from './controlRequestSource'
import { AskUserQuestionActions, AskUserQuestionContent, controlQuestion } from './controls/AskUserQuestionControl'
import { actionButtonClass, ControlActionRow } from './controls/ControlActionRow'
import { invokeControlAction } from './controls/controlResponseError'
import { canAnswerControlRequest, controlPayloadFaultNotice, controlResponseStateNotice } from './controls/controlResponseState'
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
  const { copied, copy } = useCopyButton(() => {
    const original = props.request?.originalPayload
    if (original === undefined)
      return prettifyJson(props.request?.payload)
    try {
      return prettifyJson(new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(original))
    }
    catch {
      return prettifyJson({ encoding: 'base64', content: uint8ArrayToBase64(original) })
    }
  })

  return (
    <Show when={props.request}>
      {request => (
        <div class={styles.controlBanner} data-testid="control-banner">
          <div class={styles.controlBannerActions} data-testid="control-banner-actions">
            {/*
              A turn blocked on a question is one the reader may want to abandon rather
              than answer. Denying is an answer: it reaches the agent, which carries on
              with something else. This stops the turn, and the worker withdraws the
              question with it.
            */}
            <Show when={props.onInterrupt}>
              {interrupt => (
                <IconButton
                  icon={Square}
                  size="sm"
                  onClick={() => interrupt()()}
                  title="Interrupt"
                  data-testid="control-interrupt"
                />
              )}
            </Show>
            <IconButton
              icon={copied() ? Check : Braces}
              size="sm"
              class={styles.controlBannerHoverAction}
              onClick={copy}
              title={copied() ? 'Copied' : 'Copy Raw JSON'}
              data-testid="control-copy-json"
            />
          </div>
          <Show when={controlResponseStateNotice(request().responseState)}>
            {notice => <p role="status">{notice()}</p>}
          </Show>
          <Show when={props.answerState.responseError()}>
            <p role="alert">{props.answerState.responseError()}</p>
          </Show>
          {/*
            A payload LeapMux could not read reaches the reader as the fault itself, never
            as a plugin's rendering of an empty object -- every one of those draws a
            confident "Permission Required" for a question nobody knows.
          */}
          <Show
            when={!controlPayloadFaultNotice(request().payloadFault)}
            fallback={<p role="alert">{controlPayloadFaultNotice(request().payloadFault)}</p>}
          >
            <Show when={question()} fallback={<Show when={elicitation()} fallback={<Dynamic component={pluginContent()} {...props} optionsDisabled={props.optionsDisabled || props.answerState.responsePending() || !canAnswerControlRequest(request())} agentProvider={provider()} request={request()} />}>{elicitation => <ElicitationContent {...props} optionsDisabled={props.optionsDisabled || props.answerState.responsePending() || !canAnswerControlRequest(request())} agentProvider={provider()} request={request()} elicitation={elicitation()} />}</Show>}>
              {question => (
                <AskUserQuestionContent
                  {...props}
                  optionsDisabled={props.optionsDisabled || props.answerState.responsePending() || !canAnswerControlRequest(request())}
                  agentProvider={provider()}
                  request={request()}
                  questions={question().questions}
                />
              )}
            </Show>
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
          disabled={!props.answerState.ready() || props.answerState.responsePending()}
          aria-busy={!props.answerState.ready() || props.answerState.responsePending() ? 'true' : undefined}
          style={{ display: 'contents' }}
          ref={(element) => {
            const blockUntilReady = (event: MouseEvent) => {
              if (props.answerState.ready() && !props.answerState.responsePending())
                return
              event.preventDefault()
              event.stopPropagation()
            }
            element.addEventListener('click', blockUntilReady, true)
            onCleanup(() => element.removeEventListener('click', blockUntilReady, true))
          }}
        >
          {/*
            No decision for a request nobody can read -- not even the recovery action,
            which offers to SAVE a response that was never composed. Allow would grant
            something unknown, and Deny is no safer to build: composing one needs the
            option list or the decision vocabulary the payload was carrying. The banner's
            stop is the way out, and the worker cancels from its own copy of the bytes
            (ACP-005).
          */}
          <Show when={!request().payloadFault}>
            <Show
              when={canAnswerControlRequest(request())}
              fallback={(
                <ControlActionRow primary={(
                  <button
                    class={actionButtonClass()}
                    data-testid="control-recover-response"
                    onClick={() => invokeControlAction(() => {
                      if (!props.onRecordResponse)
                        throw new Error('The response recording handler is unavailable.')
                      return props.onRecordResponse()
                    })}
                  >
                    {request().responseState === ControlResponseState.DELIVERED ? 'Save response' : 'Check status'}
                  </button>
                )}
                />
              )}
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
            </Show>
          </Show>
        </fieldset>
      )}
    </Show>
  )
}

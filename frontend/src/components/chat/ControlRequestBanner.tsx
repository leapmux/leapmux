import type { Component } from 'solid-js'
import type { ControlSurface } from './controls/controlSurface'
import type { BannerActionsProps, BannerContentProps } from './controls/types'
import type { ControlRequest } from '~/stores/control.store'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import Square from 'lucide-solid/icons/square'
import { Match, onCleanup, Show, Switch } from 'solid-js'
import { Dynamic } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { prettifyJson } from '~/lib/jsonFormat'
import * as styles from './ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './controls/AskUserQuestionControl'
import { actionButtonClass, ControlActionRow } from './controls/ControlActionRow'
import { invokeControlAction } from './controls/controlResponseError'
import { canAnswerControlRequest, controlPayloadFaultNotice, controlResponseStateNotice } from './controls/controlResponseState'
import { DialogRequestActions, DialogRequestContent } from './controls/DialogRequestControl'
import { ElicitationActions, ElicitationContent } from './controls/ElicitationControl'
import { ExitPlanModeActions } from './controls/ExitPlanModeControl'
import { GenericToolActions } from './controls/GenericToolControl'
import { PermissionDecisionActions } from './controls/PermissionDecisionActions'
import { PermissionRequestContent } from './controls/PermissionRequestContent'
import { PlanApprovalContent } from './controls/PlanApprovalContent'
import { pluginFor } from './providers/registry'
import { MarkdownPlanLayout } from './widgets/MarkdownPlanLayout'

// The banner classifies nothing. It reads the surface that its caller derived,
// which keeps ONE graph for a request that mounts a content half and an actions
// half in two different slots. That derivation must stay OUTSIDE the
// `<Show when={props.request}>` of each half. `createControlSurface` states
// why, and the composer holds it beside the active request.
/**
 * The surface, when it is of ONE kind.
 *
 * It returns the surface itself rather than its payload, because a `<Match>` treats
 * a falsy accessor value as no match -- and `plan` carries two OPTIONAL fields, so
 * its payload can legitimately be empty. Returning the variant keeps a plan with no
 * permissions and no details a match.
 */
function surfaceOf<K extends ControlSurface['kind']>(
  surface: ControlSurface | undefined,
  kind: K,
): Extract<ControlSurface, { kind: K }> | undefined {
  return surface?.kind === kind ? surface as Extract<ControlSurface, { kind: K }> : undefined
}

function questionOf(surface: ControlSurface | undefined) {
  return surfaceOf(surface, 'question')?.question
}

/** Renders control request content only (title + details), for the banner slot. */
export const ControlRequestContent: Component<BannerContentProps> = (props) => {
  // Every content surface disables its options for the same three reasons, so
  // the reasons are spelled once.
  const optionsDisabledFor = (request: ControlRequest) =>
    props.optionsDisabled || props.answerState.responsePending() || !canAnswerControlRequest(request)
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
            {/*
              ONE switch over the shared control model. Every provider used to ship a
              `ControlContent` component that dispatched to the same five bodies,
              and the five drifted apart in which fields each provider bothered to
              pass -- so a permission on one agent showed its reason and the same
              permission on the next did not.
            */}
            <Switch>
              <Match when={surfaceOf(props.controlSurface, 'question')}>
                {question => (
                  <AskUserQuestionContent
                    {...props}
                    optionsDisabled={optionsDisabledFor(request())}
                    request={request()}
                    questions={question().question.questions}
                  />
                )}
              </Match>
              <Match when={surfaceOf(props.controlSurface, 'elicitation')}>
                {elicitation => (
                  <ElicitationContent
                    {...props}
                    optionsDisabled={optionsDisabledFor(request())}
                    request={request()}
                    elicitation={elicitation().elicitation}
                  />
                )}
              </Match>
              <Match when={surfaceOf(props.controlSurface, 'plan')}>
                {plan => (
                  <Show
                    when={plan().text}
                    fallback={(
                      <PlanApprovalContent
                        request={request()}
                        {...(plan().permissions !== undefined ? { permissions: plan().permissions } : {})}
                        {...(plan().details !== undefined ? { details: plan().details } : {})}
                      />
                    )}
                  >
                    {/*
                      A request that CARRIES its plan draws it. The approval body
                      below states what the plan asks for and points at a transcript
                      row for the plan itself, which is right for every provider that
                      sends one -- and wrong for the one that does not.
                    */}
                    {text => (
                      <MarkdownPlanLayout
                        toolName="Plan"
                        title="Proposed Plan"
                        planText={text()}
                      />
                    )}
                  </Show>
                )}
              </Match>
              <Match when={surfaceOf(props.controlSurface, 'dialog')}>
                {dialog => (
                  <DialogRequestContent
                    {...props}
                    optionsDisabled={optionsDisabledFor(request())}
                    request={request()}
                    dialog={dialog().dialog}
                  />
                )}
              </Match>
              <Match when={surfaceOf(props.controlSurface, 'permission')}>
                {permission => <PermissionRequestContent request={request()} source={permission().permission} />}
              </Match>
            </Switch>
          </Show>
        </div>
      )}
    </Show>
  )
}

/** Renders control request action buttons only, for the footer slot. */
export const ControlRequestActions: Component<BannerActionsProps> = (props) => {
  const question = () => questionOf(props.controlSurface)
  const elicitation = () => surfaceOf(props.controlSurface, 'elicitation')?.elicitation
  // The actions a provider answers THIS request with, or undefined when the shared
  // switch below answers it from the model.
  const pluginActions = () => props.request
    ? pluginFor(props.agentProvider)?.controls?.controlActionsFor?.(props.request.payload)
    : undefined
  // How this provider sends ONE chosen option. Every provider whose `extractControl`
  // states options states a sender beside them, so the rejection is unreachable -- it
  // is here so a provider that adds options without one fails where the mistake is,
  // rather than drawing buttons that answer nothing.
  const sendPermissionOption = () => pluginFor(props.agentProvider)?.controls?.sendPermissionOption
    ?? (() => Promise.reject(new Error('This provider offers permission options but no way to send one.')))
  // A dialog, and how this provider answers one. A provider that sends a dialog and
  // states no responder reaches the fallback pair below.
  const dialogAnswer = () => {
    const dialog = surfaceOf(props.controlSurface, 'dialog')?.dialog
    const responder = pluginFor(props.agentProvider)?.controls?.dialogResponder
    return dialog && responder ? { dialog, responder } : undefined
  }
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
              {/*
                ONE switch over the shared control model, beside the content half's,
                and the order is what decides who answers. The question and the
                elicitation are cross-provider surfaces and come first. Then a
                provider answers its OWN request wherever `controlActionsFor`
                claims it -- Codex's decision words, Pi's plan menu, Cursor's
                create-plan verdict. Everything left is answered from the model:
                a dialog through the provider's `dialogResponder`, a plan, and a
                permission.

                The FALLBACK is the shared Allow/Deny pair, and it is what keeps the
                invariant this banner exists for: the agent's turn blocks until an
                answer reaches it, so a surface with no buttons blocks it forever. The
                content half switches over all five kinds of the closed model and this
                one answers each of them. A `dialog` of a provider that states no
                `dialogResponder` reaches the fallback, because a dialog nobody could
                dismiss would block the turn. The pair is a way OUT of such a request,
                not the right words for it.
              */}
              <Switch fallback={<GenericToolActions {...props} request={request()} />}>
                <Match when={question()}>
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
                </Match>
                <Match when={elicitation()}>
                  {elicitation => (
                    <ElicitationActions
                      {...props}
                      request={request()}
                      elicitation={elicitation()}
                    />
                  )}
                </Match>
                <Match when={pluginActions()}>
                  {ownActions => <Dynamic component={ownActions()} {...props} request={request()} />}
                </Match>
                <Match when={dialogAnswer()}>
                  {answer => <DialogRequestActions {...props} request={request()} dialog={answer().dialog} responder={answer().responder} />}
                </Match>
                <Match when={surfaceOf(props.controlSurface, 'plan')}>
                  {plan => <ExitPlanModeActions {...props} request={request()} choices={plan().choices} />}
                </Match>
                <Match when={surfaceOf(props.controlSurface, 'permission')}>
                  {permission => (
                    <Show
                      when={permission().permission.options.length > 0}
                      fallback={<GenericToolActions {...props} request={request()} />}
                    >
                      {/*
                        The runtime stated its own answers, so the reader picks one of
                        THOSE rather than the shared Allow/Deny pair -- and the layout
                        reads their KINDS, because the option-id vocabulary is each
                        agent's own.
                      */}
                      <PermissionDecisionActions
                        {...props}
                        request={request()}
                        options={() => permission().permission.options}
                        send={sendPermissionOption()}
                      />
                    </Show>
                  )}
                </Match>
              </Switch>
            </Show>
          </Show>
        </fieldset>
      )}
    </Show>
  )
}

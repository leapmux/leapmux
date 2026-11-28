import type { Component } from 'solid-js'
import type { BannerActionsProps, BannerContentProps } from './controls/types'
import type { ControlPrompt } from './model/controlPrompt'
import type { ControlRequest } from '~/stores/control.store'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import Square from 'lucide-solid/icons/square'
import { Match, onCleanup, Show, Switch } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { prettifyJson } from '~/lib/jsonFormat'
import * as styles from './ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from './controls/AskUserQuestionControl'
import { actionButtonClass, ControlActionRow } from './controls/ControlActionRow'
import { cancelQuestionDecision, questionDecision } from './controls/controlDecision'
import { invokeControlAction } from './controls/controlResponseError'
import { canAnswerControlRequest, controlPayloadFaultNotice, controlResponseStateNotice } from './controls/controlResponseState'
import { DialogRequestActions, DialogRequestContent } from './controls/DialogRequestControl'
import { ElicitationActions, ElicitationContent } from './controls/ElicitationControl'
import { ExitPlanModeActions } from './controls/ExitPlanModeControl'
import { PermissionDecisionActions } from './controls/PermissionDecisionActions'
import { PermissionRequestContent } from './controls/PermissionRequestContent'
import { PlanApprovalContent } from './controls/PlanApprovalContent'
import { MarkdownPlanLayout } from './widgets/MarkdownPlanLayout'

// The banner classifies nothing. It draws the prompt that the Worker read from the
// request, which its caller hands to the content half and to the actions half.
/**
 * The prompt, when it is of ONE kind.
 *
 * It returns the prompt itself rather than its payload, because a `<Match>` treats
 * a falsy accessor value as no match -- and `plan` carries OPTIONAL fields only, so
 * its payload can legitimately be empty. Returning the variant keeps a plan with no
 * permissions and no details a match.
 */
function surfaceOf<K extends ControlPrompt['kind']>(
  surface: ControlPrompt | undefined,
  kind: K,
): Extract<ControlPrompt, { kind: K }> | undefined {
  return surface?.kind === kind
    ? surface as Extract<ControlPrompt, { kind: K }>
    : undefined
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
      return prettifyJson({
        encoding: 'base64',
        content: uint8ArrayToBase64(original),
      })
    }
  })

  return (
    <Show when={props.request}>
      {request => (
        <div class={styles.controlBanner} data-testid="control-banner">
          <div class={styles.controlBannerActions} data-testid="control-banner-actions">
            <Show when={props.onAnswerLater}>
              {answerLater => (
                <IconButton
                  icon={ChevronDown}
                  size="sm"
                  onClick={() => answerLater()()}
                  title="Answer later"
                  data-testid="control-answer-later"
                />
              )}
            </Show>
            {/*
              A turn blocked on a question is one the reader may want to abandon rather
              than answer. Denying is an answer: it reaches the agent, which carries on
              with something else. This stops the turn: the worker withdraws a blocking
              request with it, and keeps a non-blocking question, whose answer the user
              can still send later.
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
              icon={copied()
                ? Check
                : Braces}
              size="sm"
              class={styles.controlBannerHoverAction}
              onClick={copy}
              title={copied()
                ? 'Copied'
                : 'Copy Raw JSON'}
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
            A payload LeapMux could not read reaches the reader as the fault itself: the
            Worker read no prompt from it, so nobody knows what the agent asked.
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
                    answerState={props.answerState}
                    optionsDisabled={optionsDisabledFor(request())}
                    request={request()}
                    questions={question().questions}
                    preservesSelectionNotes={question().preservesSelectionNotes ?? false}
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
                        {...(plan().permissions !== undefined
                          ? { permissions: plan().permissions }
                          : {})}
                        {...(plan().details !== undefined
                          ? { details: plan().details }
                          : {})}
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
  return (
    <Show when={props.request}>
      {request => (
        <fieldset
          data-testid="control-actions"
          disabled={!props.answerState.ready() || props.answerState.responsePending()}
          aria-busy={!props.answerState.ready() || props.answerState.responsePending()
            ? 'true'
            : undefined}
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
                    {request().responseState === ControlResponseState.DELIVERED
                      ? 'Save response'
                      : 'Check status'}
                  </button>
                )}
                />
              )}
            >
              {/*
                ONE switch over the prompt, beside the content half's. Every kind
                draws its own actions, and every one of them sends a decision: the
                Worker's provider builds the runtime's own answer from it. A
                permission prompt always states its options -- the runtime's own,
                LeapMux's allow and deny, or LeapMux's Cancel -- so the agent's turn,
                which blocks until an answer reaches it, always has one.
              */}
              <Switch>
                <Match when={surfaceOf(props.controlSurface, 'question')}>
                  {question => (
                    <AskUserQuestionActions
                      {...props}
                      request={request()}
                      questions={question().questions}
                      preservesSelectionNotes={question().preservesSelectionNotes ?? false}
                      onSubmitAnswers={() => props.onDecide(questionDecision(props.answerState, question()))}
                      onReject={message => props.onDecide(cancelQuestionDecision(message))}
                    />
                  )}
                </Match>
                <Match when={surfaceOf(props.controlSurface, 'elicitation')}>
                  {elicitation => (
                    <ElicitationActions
                      {...props}
                      request={request()}
                      elicitation={elicitation().elicitation}
                    />
                  )}
                </Match>
                <Match when={surfaceOf(props.controlSurface, 'dialog')}>
                  {dialog => <DialogRequestActions {...props} request={request()} dialog={dialog().dialog} />}
                </Match>
                <Match when={surfaceOf(props.controlSurface, 'plan')}>
                  {plan => <ExitPlanModeActions {...props} request={request()} choices={plan().choices} />}
                </Match>
                <Match when={surfaceOf(props.controlSurface, 'permission')}>
                  {permission => (
                    <PermissionDecisionActions
                      {...props}
                      request={request()}
                      options={() => permission().permission.options}
                      independentOptions={permission().permission.independentOptions ?? false}
                    />
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

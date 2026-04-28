import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'
import { createMemo, createSignal, Match, Show, Switch, untrack } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { decodeControlResponseBehavior } from '~/utils/controlResponse'
import * as styles from '../../ControlRequestBanner.css'
import { AskUserQuestionActions, AskUserQuestionContent } from '../../controls/AskUserQuestionControl'
import { piQuestionsFromPayload } from './askUserQuestion'
import {
  piAskAnswerValue,
  piCancelResponse,
  piConfirmResponse,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'
import { PI_DIALOG_METHOD } from './protocol'

function timeoutHint(payload: Record<string, unknown>): string | null {
  const t = pickNumber(payload, 'timeout')
  if (t == null || t <= 0)
    return null
  return `Auto-resolves in ${Math.round(t / 1000)}s if no response.`
}

interface PiButtonShape {
  denyLabel: string
  denyClick: () => void
  primaryLabel: string
  primaryClick: () => void
}

/** Pi-specific control request content (title + body per dialog method). */
export const PiControlContent: Component<ContentProps> = (props) => {
  const payload = () => props.request.payload
  const method = createMemo(() => pickString(payload(), 'method', undefined))
  const title = createMemo(() => pickString(payload(), 'title') || 'Approval Required')
  const message = createMemo(() => pickString(payload(), 'message'))
  const placeholder = createMemo(() => pickString(payload(), 'placeholder'))
  const prefill = createMemo(() => pickString(payload(), 'prefill'))
  const hint = createMemo(() => timeoutHint(payload()))
  const questions = createMemo(() => piQuestionsFromPayload(payload()))
  return (
    <Show
      when={method() === PI_DIALOG_METHOD.Select}
      fallback={(
        <>
          <div class={styles.controlBannerTitle}>{title()}</div>
          <Switch>
            <Match when={method() === PI_DIALOG_METHOD.Confirm}>
              <Show when={message()}>
                <div class={styles.bannerReason}>{message()}</div>
              </Show>
            </Match>
            <Match when={method() === PI_DIALOG_METHOD.Input}>
              <Show when={placeholder()}>
                <div class={styles.bannerHint}>
                  {`hint: ${placeholder()}`}
                </div>
              </Show>
            </Match>
            <Match when={method() === PI_DIALOG_METHOD.Editor}>
              <Show when={prefill()}>
                <pre class={styles.bannerCodeBlock}>{prefill()}</pre>
              </Show>
            </Match>
          </Switch>
          <Show when={hint()}>
            <div class={styles.bannerHint}>{hint()}</div>
          </Show>
        </>
      )}
    >
      <AskUserQuestionContent
        request={props.request}
        askState={props.askState}
        optionsDisabled={props.optionsDisabled}
        agentProvider={AgentProvider.PI}
        questions={questions()}
      />
      <Show when={hint()}>
        <div class={styles.bannerHint}>{hint()}</div>
      </Show>
    </Show>
  )
}

/** Pi-specific control request action buttons (per dialog method). */
export const PiControlActions: Component<ActionsProps> = (props) => {
  const payload = () => props.request.payload
  const method = createMemo(() => pickString(payload(), 'method', undefined))
  const placeholder = createMemo(() => pickString(payload(), 'placeholder'))
  const requestId = () => props.request.requestId
  const agentId = () => props.request.agentId
  const questions = createMemo(() => piQuestionsFromPayload(payload()))

  const handleConfirm = (confirmed: boolean) => {
    sendPiExtensionResponse(agentId(), props.onRespond, piConfirmResponse(requestId(), confirmed))
  }
  const handleCancel = () => {
    sendPiExtensionResponse(agentId(), props.onRespond, piCancelResponse(requestId()))
  }
  const sendValue = (value: string) => {
    sendPiExtensionResponse(agentId(), props.onRespond, piValueResponse(requestId(), value))
  }

  // Intercepts AskUserQuestionActions' default `buildAllowResponse` /
  // `buildDenyResponse` envelopes (the wire format Claude / Codex use)
  // and translates them into Pi's extension_ui_response shape. We
  // inspect the synthesized response to detect the deny path; otherwise
  // we pull the answer from askState and ship it as a Pi value response.
  const selectOnRespond = async (_agentId: string, content: Uint8Array) => {
    if (decodeControlResponseBehavior(content) === 'deny') {
      await sendPiExtensionResponse(agentId(), props.onRespond, piCancelResponse(requestId()))
      return
    }

    const value = piAskAnswerValue(props.askState)
    await sendPiExtensionResponse(
      agentId(),
      props.onRespond,
      value.trim() ? piValueResponse(requestId(), value) : piCancelResponse(requestId()),
    )
  }

  // Local input state for `input` and `editor` dialogs. The initial value is
  // snapshotted from the request's prefill once at mount; subsequent payload
  // identity changes do not reset the user's in-progress text.
  const [localText, setLocalText] = createSignal(
    untrack(() => pickString(props.request.payload, 'prefill')),
  )

  // The dialog method drives both the deny/primary button labels and the
  // primary handler. Computing the four pieces as one memo keeps a single
  // ButtonGroup at the bottom of the footer and avoids three near-identical
  // `<ButtonGroup>` blocks across the per-method Match arms.
  const buttons = createMemo<PiButtonShape>(() => {
    switch (method()) {
      case PI_DIALOG_METHOD.Confirm:
        return {
          denyLabel: 'Deny',
          denyClick: () => handleConfirm(false),
          primaryLabel: 'Approve',
          primaryClick: () => handleConfirm(true),
        }
      case PI_DIALOG_METHOD.Input:
      case PI_DIALOG_METHOD.Editor:
        return {
          denyLabel: 'Cancel',
          denyClick: handleCancel,
          primaryLabel: 'Send',
          primaryClick: () => sendValue(localText()),
        }
      default:
        return {
          denyLabel: 'Cancel',
          denyClick: handleCancel,
          primaryLabel: 'Acknowledge',
          primaryClick: () => handleConfirm(true),
        }
    }
  })

  return (
    <Show
      when={method() === PI_DIALOG_METHOD.Select}
      fallback={(
        <div class={styles.controlFooter}>
          <div class={styles.controlFooterLeft}>
            {props.infoTrigger}
          </div>
          <div class={styles.controlFooterRight}>
            <Switch>
              <Match when={method() === PI_DIALOG_METHOD.Input}>
                <input
                  type="text"
                  placeholder={placeholder()}
                  value={localText()}
                  onInput={e => setLocalText((e.currentTarget as HTMLInputElement).value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      sendValue(localText())
                    }
                  }}
                  data-testid="pi-input"
                  style={{ 'flex': '1 1 auto', 'min-width': '200px' }}
                />
              </Match>
              <Match when={method() === PI_DIALOG_METHOD.Editor}>
                <textarea
                  value={localText()}
                  onInput={e => setLocalText((e.currentTarget as HTMLTextAreaElement).value)}
                  data-testid="pi-editor"
                  rows={4}
                  style={{ 'flex': '1 1 auto', 'min-width': '300px', 'resize': 'vertical' }}
                />
              </Match>
            </Switch>
            <ButtonGroup>
              <button class="outline" onClick={buttons().denyClick} data-testid="control-deny-btn">{buttons().denyLabel}</button>
              <button onClick={buttons().primaryClick} data-testid="control-allow-btn">{buttons().primaryLabel}</button>
            </ButtonGroup>
          </div>
        </div>
      )}
    >
      <AskUserQuestionActions
        {...props}
        onRespond={selectOnRespond}
        agentProvider={AgentProvider.PI}
        questions={questions()}
      />
    </Show>
  )
}

import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'
import { createMemo, Match, Show, Switch, untrack } from 'solid-js'
import { PI_DIALOG_METHOD } from '~/generated/contracts/pi-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import * as styles from '../../ControlRequestBanner.css'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { invokeControlAction } from '../../controls/controlResponseError'
import { PlanApprovalContent } from '../../controls/PlanApprovalContent'
import { createControlChoice } from '../../controls/types'
import {
  piCancelResponse,
  piConfirmResponse,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'
import { PiPlanApprovalActions } from './planApproval'
import { isPiPlanApproval, piPlanApprovalDetails } from './planRequest'

function timeoutHint(payload: Record<string, unknown>): string | null {
  const t = pickNumber(payload, 'timeout')
  if (t == null || t <= 0)
    return null
  return `Auto-resolves in ${Math.round(t / 1000)}s if no response.`
}

interface PiButtonShape {
  denyLabel: string
  denyClick: () => Promise<void>
  primaryLabel: string
  primaryClick: () => Promise<void>
}

function createPiDialogText(props: Pick<ContentProps, 'request' | 'answerState'>) {
  return createControlChoice(() => props.answerState, 'pi-dialog-text', untrack(() => pickString(props.request.payload, 'prefill')))
}

/**
 * Render dialogs that Pi exports through RPC.
 * Goal task approval needs an upstream fallback for its custom terminal UI:
 * https://github.com/tmonk/pi-goal-x/issues/52
 */
const PiDialogContent: Component<ContentProps> = (props) => {
  const payload = () => props.request.payload
  const method = createMemo(() => pickString(payload(), 'method', undefined))
  const title = createMemo(() => pickString(payload(), 'title') || 'Approval Required')
  const message = createMemo(() => pickString(payload(), 'message'))
  const placeholder = createMemo(() => pickString(payload(), 'placeholder'))
  const hint = createMemo(() => timeoutHint(payload()))
  const text = createPiDialogText(props)
  return (
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
      </Switch>
      <Show when={method() === PI_DIALOG_METHOD.Editor}>
        <textarea
          aria-label={title()}
          value={text.choice() ?? ''}
          disabled={props.optionsDisabled}
          onInput={event => text.setChoice(event.currentTarget.value)}
          data-testid="pi-editor"
          rows={6}
          wrap="off"
          style={{ 'width': '100%', 'min-width': '0', 'max-width': '100%', 'max-height': '24rem', 'resize': 'vertical', 'font-family': 'var(--font-mono)' }}
        />
      </Show>
      <Show when={hint()}>
        <div class={styles.bannerHint}>{hint()}</div>
      </Show>
    </>
  )
}

/** Pi-specific control request action buttons (per dialog method). */
const PiDialogActions: Component<ActionsProps> = (props) => {
  const payload = () => props.request.payload
  const method = createMemo(() => pickString(payload(), 'method', undefined))
  const placeholder = createMemo(() => pickString(payload(), 'placeholder'))
  const requestId = () => props.request.requestId

  const handleConfirm = (confirmed: boolean) => {
    return sendPiExtensionResponse(props.onRespond, piConfirmResponse(requestId(), confirmed))
  }
  const handleCancel = () => {
    return sendPiExtensionResponse(props.onRespond, piCancelResponse(requestId()))
  }
  const sendValue = (value: string) => {
    return sendPiExtensionResponse(props.onRespond, piValueResponse(requestId(), value))
  }

  // Shared answer state retains edits across remounts, including an empty value.
  const text = createPiDialogText(props)
  const localText = () => text.choice() ?? ''
  const setLocalText = text.setChoice

  // The dialog method supplies labels and handlers for the shared decision footer.
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
    <ControlDecisionFooter
      hasEditorContent={false}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: buttons().denyLabel, testId: 'control-deny-btn', onSelect: buttons().denyClick }}
      positiveAction={{ label: buttons().primaryLabel, testId: 'control-allow-btn', onSelect: buttons().primaryClick }}
      leading={(
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
                  invokeControlAction(() => sendValue(localText()))
                }
              }}
              data-testid="pi-input"
              style={{ 'flex': '1 1 200px', 'min-width': '0', 'max-width': '100%' }}
            />
          </Match>
        </Switch>
      )}
    />
  )
}

export const PiControlContent: Component<ContentProps> = props => (
  <Show when={isPiPlanApproval(props.request.payload)} fallback={<PiDialogContent {...props} />}>
    <PlanApprovalContent request={props.request} details={piPlanApprovalDetails(props.request.payload)} />
  </Show>
)

export const PiControlActions: Component<ActionsProps> = props => (
  <Show when={isPiPlanApproval(props.request.payload)} fallback={<PiDialogActions {...props} />}>
    <PiPlanApprovalActions {...props} />
  </Show>
)

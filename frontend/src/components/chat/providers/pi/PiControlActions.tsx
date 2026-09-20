import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'
import { createMemo, Match, Show, Switch } from 'solid-js'
import { PI_DIALOG_METHOD } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { invokeControlAction } from '../../controls/controlResponseError'
import { createDialogText } from '../../controls/DialogRequestControl'
import {
  piCancelResponse,
  piConfirmResponse,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'
import { PiPlanApprovalActions } from './PiPlanApprovalActions'
import { isPiPlanApproval } from './planRequest'

interface PiButtonShape {
  denyLabel: string
  denyClick: () => Promise<void>
  primaryLabel: string
  primaryClick: () => Promise<void>
}

/**
 * The dialog's editor state, read through the SHARED key.
 *
 * The content half writes it and this half sends it. They held two different keys
 * once, and the send then shipped an empty value for a textarea the reader had
 * filled -- see `DIALOG_TEXT_CHOICE_ID`.
 */
function createPiDialogText(props: Pick<ContentProps, 'request' | 'answerState'>) {
  return createDialogText(props, () => pickString(props.request.payload, 'prefill'))
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

export const PiControlActions: Component<ActionsProps> = props => (
  <Show when={isPiPlanApproval(props.request.payload)} fallback={<PiDialogActions {...props} />}>
    <PiPlanApprovalActions {...props} />
  </Show>
)

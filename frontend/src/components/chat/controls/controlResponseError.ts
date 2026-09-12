import { showWarnToast } from '~/components/common/Toast'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { formatErrorMessage } from '~/lib/errors'

export class ControlResponseDeliveryError extends Error {
  constructor(readonly state: ControlResponseState, detail: string) {
    const prefix = state === ControlResponseState.DELIVERED
      ? 'Could not save the response'
      : state === ControlResponseState.READY
        ? 'The response was not sent'
        : 'Could not complete the response'
    super(`${prefix}: ${detail}`)
  }
}

export function controlResponseErrorMessage(error: unknown): string {
  return error instanceof ControlResponseDeliveryError
    ? error.message
    : `Could not complete the response: ${formatErrorMessage(error)}`
}

/** The request banner or a toast already reports this delivery error. */
export class ReportedControlResponseError extends Error {
  constructor(cause: unknown) {
    super(controlResponseErrorMessage(cause), { cause })
  }
}

/** Catch errors at the action boundary without changing response promise semantics. */
export function invokeControlAction(action: () => void | Promise<void>): void {
  const report = (error: unknown) => {
    if (!(error instanceof ReportedControlResponseError))
      showWarnToast('Could not complete the action', error)
  }
  try {
    const result = action()
    if (result)
      void result.catch(report)
  }
  catch (error) {
    report(error)
  }
}

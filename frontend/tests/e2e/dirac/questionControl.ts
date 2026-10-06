import type { NativeControlFrame } from '../helpers/nativeControlWatch'
import { MCP_ELICITATION_METHOD } from '../../../src/generated/contracts/mcp-elicitation'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'

/**
 * Return the one native question request that a control watch saw.
 *
 * Dirac asks a question through ACP `elicitation/create`, and the Worker publishes that JSON-RPC request as the payload
 * of the control. The request must hold `question` in its parameters. A replay of the same request counts once. No
 * question request, a second one, or one without the question fails.
 */
export function diracQuestionControl(controls: readonly NativeControlFrame[], question: string): NativeControlFrame {
  if (question.trim() === '')
    throw new Error('The native Dirac question request needs the text of its question.')
  const asked = new Map<string, NativeControlFrame>()
  for (const frame of controls) {
    if (frame.responseState === ControlResponseState.READY && frame.payload.method === MCP_ELICITATION_METHOD.ACP)
      asked.set(frame.requestId, frame)
  }
  if (asked.size !== 1)
    throw new Error(`The native Dirac question needs exactly one elicitation request, not ${asked.size}.`)
  const frame = [...asked.values()][0]!
  if (!JSON.stringify(frame.payload.params ?? null).includes(question))
    throw new Error('The native Dirac elicitation request does not hold the question.')
  return frame
}

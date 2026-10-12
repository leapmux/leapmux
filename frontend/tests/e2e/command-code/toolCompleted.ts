import { COMMAND_CODE_EVENT, COMMAND_CODE_FRAME_KIND } from '../../../src/generated/contracts/commandcode-protocol'
import { pickObject } from '../../../src/lib/jsonPick'

/**
 * Select the stored frame of the `tool_completed` event that ends call `callId` of tool `toolName`. Command Code
 * wraps each native event in a frame of the `event` kind, and the event states its tool and its call.
 */
export function commandCodeToolCompleted(toolName: string, callId: string): (frame: Record<string, unknown>) => boolean {
  if (!toolName || !callId)
    throw new Error('The Command Code completion requires a tool name and a call ID.')
  return (frame) => {
    const event = pickObject(frame, 'event')
    return frame.type === COMMAND_CODE_FRAME_KIND.Event && event?.type === COMMAND_CODE_EVENT.ToolCompleted
      && event.toolName === toolName && event.toolCallId === callId
  }
}

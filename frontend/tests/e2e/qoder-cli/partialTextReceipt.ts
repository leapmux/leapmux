import { QODER_FRAME_KIND } from '../../../src/generated/contracts/qoder-protocol'
import { isObject } from '../../../src/lib/jsonPick'

/** Read only root text deltas that the native process sent to the Worker. */
export function hasQoderPartialTextReceipt(frames: readonly unknown[], text: string): boolean {
  if (!text)
    throw new Error('A Qoder text receipt requires nonempty text.')
  const rootFrames = frames.filter(isObject).filter(frame => frame.type === QODER_FRAME_KIND.StreamEvent
    && (frame.parent_tool_use_id === undefined || frame.parent_tool_use_id === null))
  return rootFrames.flatMap((frame) => {
    const event = frame.event
    if (!isObject(event) || event.type !== 'content_block_delta' || !isObject(event.delta)
      || event.delta.type !== 'text_delta' || typeof event.delta.text !== 'string') {
      return []
    }
    return [event.delta.text]
  }).join('').includes(text)
}

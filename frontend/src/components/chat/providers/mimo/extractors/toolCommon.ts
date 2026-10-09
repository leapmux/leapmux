import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { parseDataImageUrl } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../../registry'

/** One stored MiMo event: `{type, properties}`, which the worker keeps verbatim. */
export interface MiMoEvent {
  type: string
  properties: Record<string, unknown>
}

/** Read a stored row as a MiMo event, or null for a row of another shape. */
export function mimoEvent(parsed: unknown): MiMoEvent | null {
  if (!isObject(parsed))
    return null
  const type = pickString(parsed, 'type')
  const properties = pickObject(parsed, 'properties')
  if (!type || !properties)
    return null
  return { type, properties }
}

/** The part of a `message.part.updated` event, or null for any other row. */
export function mimoPart(parsed: unknown): Record<string, unknown> | null {
  const event = mimoEvent(parsed)
  if (!event || event.type !== MIMO_EVENT.MessagePartUpdated)
    return null
  return pickObject(event.properties, 'part') ?? null
}

/**
 * One tool part, with every field the readers need already read.
 *
 * MiMo writes the complete call state on each part update.
 * One frame therefore supplies the arguments for its current status.
 * These frames state the input:
 * - The opening frame.
 * - Each progress frame.
 * - The final frame.
 * The final frame also supplies the output and metadata.
 */
export interface MiMoToolPart {
  partId: string
  messageId: string
  sessionId: string
  callId: string
  tool: string
  /** `pending`, `running`, `completed` or `error`. */
  status: string
  input: Record<string, unknown>
  /** What the tool answered the model with. Empty until the call completes. */
  output: string
  /** Why the call failed. Empty unless the status is `error`. */
  error: string
  /** The one-line title MiMo wrote for the call, such as a file path. */
  title: string
  /** Structured results can include a diff or count. They can include an exit code also. */
  metadata: Record<string, unknown>
  /** The files the tool attached for the model, such as the image a read returned. */
  attachments: Record<string, unknown>[]
}

/** Read one tool part out of a stored row, or null for a row that is not one. */
export function mimoToolPart(parsed: unknown): MiMoToolPart | null {
  const part = mimoPart(parsed)
  if (!part || pickString(part, 'type') !== MIMO_PART_TYPE.Tool)
    return null
  const partId = pickString(part, 'id')
  const messageId = pickString(part, 'messageID')
  const sessionId = pickString(part, 'sessionID')
  const callId = pickString(part, 'callID')
  const state = pickObject(part, 'state')
  if (!partId || !callId || !state)
    return null
  return {
    partId,
    messageId,
    sessionId,
    callId,
    tool: pickString(part, 'tool'),
    status: pickString(state, 'status'),
    input: pickObject(state, 'input') ?? {},
    output: pickString(state, 'output'),
    error: pickString(state, 'error'),
    title: pickString(state, 'title'),
    metadata: pickObject(state, 'metadata') ?? {},
    attachments: Array.isArray(state.attachments) ? state.attachments.filter(isObject) : [],
  }
}

/** True when the part reached a final state: completed, or ended in an error. */
export function mimoToolFinished(part: Pick<MiMoToolPart, 'status'>): boolean {
  return part.status === MIMO_TOOL_STATUS.Completed || part.status === MIMO_TOOL_STATUS.Error
}

/**
 * Where one tool row sits in its span.
 *
 * The Worker stores the first RUNNING frame as the request.
 * It stores the final frame as the result.
 * If the turn ends before the call completes, the Worker closes the span with the last frame.
 * That frame still reports running. LeapMux's completion column determines its row role.
 * A pending frame states no input and never enters the transcript.
 */
export function mimoToolSpanRole(part: MiMoToolPart, completion: MessageCompletion | undefined): ToolSpanRole {
  if (mimoToolFinished(part) || retainedRowIsFinal(completion))
    return 'result'
  if (part.status === MIMO_TOOL_STATUS.Running)
    return 'request'
  return 'other'
}

/**
 * The pictures a tool attached for the model.
 *
 * MiMo attaches a file through a part with a `data:` URL.
 * An image read and `view_image` both use this form.
 * The row shows the image that the model received.
 * The attachment identifies its file by basename only. The viewer cannot open that basename.
 * The read reader therefore supplies the path from the call's arguments.
 */
export function mimoToolImages(part: MiMoToolPart): ImageResultSource[] {
  return part.attachments.flatMap((attachment) => {
    const parsed = parseDataImageUrl(pickString(attachment, 'url'))
    if (!parsed || !parsed.mimeType.startsWith('image/'))
      return []
    return [{ mimeType: parsed.mimeType, data: parsed.base64 }]
  })
}

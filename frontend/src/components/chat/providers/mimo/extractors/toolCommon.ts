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
 * MiMo writes the whole call state on each update of the part, so ONE frame holds the
 * call's arguments whatever its status: the opening frame, a progress frame and the
 * final frame each state the input, and the final one adds the output and the metadata.
 */
export interface MiMoToolPart {
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
  /** The tool's own structured result: a diff, a count, an exit code. */
  metadata: Record<string, unknown>
  /** The files the tool attached for the model, such as the image a read returned. */
  attachments: Record<string, unknown>[]
}

/** Read one tool part out of a stored row, or null for a row that is not one. */
export function mimoToolPart(parsed: unknown): MiMoToolPart | null {
  const part = mimoPart(parsed)
  if (!part || pickString(part, 'type') !== MIMO_PART_TYPE.Tool)
    return null
  const callId = pickString(part, 'callID')
  const state = pickObject(part, 'state')
  if (!callId || !state)
    return null
  return {
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
 * The worker persists a call's first RUNNING frame as the request and its final frame
 * as the result. A turn that ends while the call runs closes the span with the call's
 * last frame, which still reads as running, so LeapMux's own completion column decides
 * that row. A pending frame never reaches the transcript: it states no input yet.
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
 * MiMo attaches a file as a part with a `data:` URL. A read of an image file and
 * `view_image` both answer this way, and the row shows the picture the model saw.
 * The attachment names its file by BASENAME alone, which the viewer cannot open, so
 * the reader of a read call supplies the path from the call's own arguments.
 */
export function mimoToolImages(part: MiMoToolPart): ImageResultSource[] {
  return part.attachments.flatMap((attachment) => {
    const parsed = parseDataImageUrl(pickString(attachment, 'url'))
    if (!parsed || !parsed.mimeType.startsWith('image/'))
      return []
    return [{ mimeType: parsed.mimeType, data: parsed.base64 }]
  })
}

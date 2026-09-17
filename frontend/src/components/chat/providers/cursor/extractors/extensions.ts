import type { ImageResultSource } from '~/lib/imageBlocks'
import type { TodoItem } from '~/models/todo'
import { CURSOR_EXTENSION_FRAME, CURSOR_METHOD, CURSOR_SUPPLEMENT } from '~/generated/contracts/cursor-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { rawTodosToItems } from '~/models/todo'

/**
 * One `cursor/*` extension frame, as the worker stored it on a tool row.
 *
 * Cursor sends the frame as a JSON-RPC REQUEST, one per finished call, right after
 * the update that completed the same `toolCallId`. The worker answers it and writes
 * it into that row's supplemental content, because the frame carries fields the tool
 * call itself never states. See `cursor_extensions.go`.
 */
export interface CursorExtension {
  method: string
  params: Record<string, unknown>
}

/** Read the stored frame off one row's supplemental content, or null for every other row. */
export function cursorExtension(supplemental: Record<string, unknown> | undefined): CursorExtension | null {
  const stored = pickObject(supplemental, CURSOR_SUPPLEMENT.Extension)
  const method = pickString(stored, CURSOR_EXTENSION_FRAME.Method)
  if (!method)
    return null
  return { method, params: pickObject(stored, CURSOR_EXTENSION_FRAME.Params) ?? {} }
}

/**
 * The to-do list one `updateTodos` row shows.
 *
 * Two sources, and the stored frame wins. Cursor spells a status twice: the frame
 * normalizes it (`in_progress`), and the tool call's own `rawInput.todos` repeats the
 * protobuf enum NAME (`TODO_STATUS_IN_PROGRESS`). A row written before the worker
 * stored these frames carries the second form alone, so both are read.
 *
 * The frame's `merge` flag is deliberately not drawn. The row shows what Cursor
 * reported in that call, which is what the reader saw happen; the sidebar owns the
 * whole list, and the worker applies the same flag there.
 */
export function cursorTodoItems(extension: CursorExtension | null, input: Record<string, unknown>): TodoItem[] {
  const fromFrame = extension?.method === CURSOR_METHOD.UpdateTodos ? extension.params[CURSOR_EXTENSION_FRAME.Todos] : undefined
  if (Array.isArray(fromFrame))
    return rawTodosToItems(fromFrame)
  const raw = input.todos
  if (!Array.isArray(raw))
    return []
  return rawTodosToItems(raw.map(entry => isObject(entry) ? { ...entry, status: cursorTodoStatusWord(entry.status) } : entry))
}

/**
 * Fold Cursor's protobuf enum NAME onto the word every other list uses.
 *
 * `rawInput.todos` carries `TODO_STATUS_IN_PROGRESS`, which the shared normalizer
 * reads as `pending` -- the state it claims the least about -- so every row of an
 * older transcript drew as unstarted. A value that is already the word passes
 * through unchanged.
 */
function cursorTodoStatusWord(status: unknown): unknown {
  if (typeof status !== 'string' || !status.startsWith('TODO_STATUS_'))
    return status
  return status.slice('TODO_STATUS_'.length).toLowerCase()
}

/**
 * The images one `generateImage` row shows.
 *
 * `filePath` is where the runtime WROTE the image, which the call's own
 * `rawInput.filename` only requested. The reference images are the inputs the
 * prompt pointed at, and they are shown after the result so the reader sees the
 * output first.
 *
 * Each source carries a path and no bytes: the viewer opens the file through the
 * worker, which is the same route a Claude `Read` of an image takes.
 */
export function cursorGeneratedImages(extension: CursorExtension | null): ImageResultSource[] {
  if (extension?.method !== CURSOR_METHOD.GenerateImage)
    return []
  const description = pickString(extension.params, 'description')
  const produced = pickString(extension.params, 'filePath')
  const references = Array.isArray(extension.params.referenceImagePaths) ? extension.params.referenceImagePaths : []
  const sources: ImageResultSource[] = produced ? [{ filePath: produced, ...(description ? { description } : {}) }] : []
  for (const reference of references) {
    if (typeof reference === 'string' && reference)
      sources.push({ filePath: reference, description: 'Reference image' })
  }
  return sources
}

/**
 * What one `task` row states about the subagent that ran.
 *
 * The tool call carries the prompt, the description and the REQUESTED type. The
 * frame adds the model that answered, the runtime's own agent id and the measured
 * duration, so the row reports the run rather than the request.
 */
export function cursorTaskDetails(extension: CursorExtension | null): Record<string, unknown> {
  if (extension?.method !== CURSOR_METHOD.Task)
    return {}
  const details: Record<string, unknown> = {}
  for (const key of ['subagentType', 'model', 'agentId', 'durationMs'] as const) {
    const value = extension.params[key]
    if (value !== undefined && value !== null && value !== '')
      details[key] = value
  }
  return details
}

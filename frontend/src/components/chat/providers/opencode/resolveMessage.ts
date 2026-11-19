import type { ParsedMessageContent } from '~/lib/messageParser'
import { OPENCODE_NATIVE_EVENT } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

const TOOL_EVENTS = new Set<string>([
  OPENCODE_NATIVE_EVENT.ToolInputStarted,
  OPENCODE_NATIVE_EVENT.ToolCalled,
  OPENCODE_NATIVE_EVENT.ToolProgress,
  OPENCODE_NATIVE_EVENT.ToolSuccess,
  OPENCODE_NATIVE_EVENT.ToolFailed,
])

/** Merge native start and call records into a display copy of the same call. */
export function resolveOpenCodeMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  if (!original || !TOOL_EVENTS.has(pickString(original, 'type')))
    return undefined
  const data = pickObject(original, 'data')
  if (!data || typeof data.id !== 'string'
    || typeof data.assistantMessageID !== 'string' || !data.assistantMessageID
    || typeof data.sessionID !== 'string' || !data.sessionID
    || !Array.isArray(parsed.supplementalContent)) {
    return undefined
  }
  let input: Record<string, unknown> | undefined
  let name: string | undefined
  let progress: Record<string, unknown> | undefined
  for (const item of parsed.supplementalContent) {
    if (!isObject(item))
      continue
    const supplement = pickObject(item, 'data')
    if (!supplement || supplement.id !== data.id
      || supplement.assistantMessageID !== data.assistantMessageID
      || supplement.sessionID !== data.sessionID) {
      continue
    }
    if (item.type === OPENCODE_NATIVE_EVENT.ToolInputStarted && typeof supplement.name === 'string')
      name = supplement.name
    if (item.type === OPENCODE_NATIVE_EVENT.ToolCalled && isObject(supplement.input)) {
      input = { ...supplement.input }
    }
    if (item.type === OPENCODE_NATIVE_EVENT.ToolProgress && isObject(supplement.metadata))
      progress = { ...supplement.metadata }
  }
  if (name === undefined && input === undefined && progress === undefined)
    return undefined
  return {
    ...original,
    data: {
      ...(name !== undefined
        ? { name }
        : {}),
      ...(input !== undefined
        ? { input }
        : {}),
      ...data,
      ...(progress !== undefined
        ? {
            metadata: {
              ...progress,
              ...pickObject(data, 'metadata'),
            },
          }
        : {}),
    },
  }
}

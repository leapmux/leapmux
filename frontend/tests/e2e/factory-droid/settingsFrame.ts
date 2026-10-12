import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'

export interface DroidNativeSettingsUpdate {
  requestId: string | undefined
  modelId: string | undefined
  reasoningEffort: string | undefined
  interactionMode: string | undefined
  autonomyLevel: string | undefined
}

/** Read every native settings event in a stored row. */
export function parseDroidNativeSettingsUpdates(raw: string): DroidNativeSettingsUpdate[] {
  let frame: unknown
  try {
    frame = JSON.parse(raw)
  }
  catch {
    return []
  }
  if (!isObject(frame))
    return []
  if (pickString(frame, 'type') === 'notification_thread') {
    const messages = frame.messages
    return Array.isArray(messages) ? messages.flatMap(readDroidSettingsFrame) : []
  }
  return readDroidSettingsFrame(frame)
}

function readDroidSettingsFrame(frame: unknown): DroidNativeSettingsUpdate[] {
  if (!isObject(frame) || pickString(frame, 'type') !== 'settings_updated')
    return []
  const settings = pickObject(frame, 'settings')
  if (!settings)
    return []
  return [{
    requestId: pickString(frame, 'requestId', undefined),
    modelId: pickString(settings, 'modelId', undefined),
    reasoningEffort: pickString(settings, 'reasoningEffort', undefined),
    interactionMode: pickString(settings, 'interactionMode', undefined),
    autonomyLevel: pickString(settings, 'autonomyLevel', undefined),
  }]
}

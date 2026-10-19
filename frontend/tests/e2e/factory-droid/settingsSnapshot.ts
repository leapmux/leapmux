import { isObject, pickString } from '../../../src/lib/jsonPick'

export interface DroidNativeSettingsSnapshot {
  modelId: string | undefined
  reasoningEffort: string | undefined
  interactionMode: string | undefined
  autonomyLevel: string | undefined
}

/** Read the effective settings that Droid stores beside its native session transcript. */
export function parseDroidNativeSettingsSnapshot(raw: string): DroidNativeSettingsSnapshot {
  const settings: unknown = JSON.parse(raw)
  if (!isObject(settings))
    throw new Error('The Droid native settings file must contain an object.')
  return {
    modelId: pickString(settings, 'model', undefined),
    reasoningEffort: pickString(settings, 'reasoningEffort', undefined),
    interactionMode: pickString(settings, 'interactionMode', undefined),
    autonomyLevel: pickString(settings, 'autonomyLevel', undefined),
  }
}

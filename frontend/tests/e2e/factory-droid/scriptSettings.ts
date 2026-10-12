import { isObject } from '../../../src/lib/jsonPick'

/** Construct native flat settings and the private Script feature snapshot. */
export function droidScriptConfiguration(settings: unknown): {
  settings: Record<string, unknown>
  snapshot: { flags: { script_tools: true }, configs: Record<string, never> }
} {
  if (!isObject(settings))
    throw new Error('The native Droid Script profile requires a settings object.')
  // The file loader wraps the entire flat file as hierarchy.settings.general.
  return {
    settings: { ...settings, toolExecutionMode: 'direct_and_script' },
    snapshot: { flags: { script_tools: true }, configs: {} },
  }
}

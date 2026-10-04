import type { MockModelRule, MockModelStep } from '../helpers/mockModelScript'

/** Select the native parent continuation after this child completes. */
export function diracChildResultRule(taskMarker: string, finalStep: MockModelStep): MockModelRule {
  if (!taskMarker.trim())
    throw new Error('The native child result requires a task marker.')
  const encodedMarker = JSON.stringify(taskMarker).slice(1, -1)
  const markerPattern = encodedMarker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const resultCharacters = String.raw`(?:[^"\\]|\\.)*`
  return {
    name: `dirac-native-child-notice-${taskMarker}`,
    // The request keeps old results. Match the current marker inside one JSON result string.
    when: { body: `"Subagent results:${resultCharacters}${markerPattern}${resultCharacters}"` },
    respond: finalStep,
    once: true,
  }
}

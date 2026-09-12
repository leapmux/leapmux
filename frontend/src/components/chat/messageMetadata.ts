import { SESSION_INFO_KEY } from '~/generated/contracts/session-info'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { isObject } from '~/lib/jsonPick'

/** Apply validated worker metadata after the provider verifies the matching envelope. */
export function applyMessageMetadata(original: Record<string, unknown>, supplemental: Record<string, unknown>): Record<string, unknown> {
  const fields: Record<string, unknown> = {}
  for (const key of [MESSAGE_METADATA_FIELD.DurationMs, MESSAGE_METADATA_FIELD.ToolUses]) {
    const value = supplemental[key]
    if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0)
      fields[key] = value
  }
  const cost = supplemental[SESSION_INFO_KEY.TotalCostUsd]
  if (typeof cost === 'number' && Number.isFinite(cost) && cost >= 0)
    fields[SESSION_INFO_KEY.TotalCostUsd] = cost
  if (isObject(supplemental[SESSION_INFO_KEY.ContextUsage]))
    fields[SESSION_INFO_KEY.ContextUsage] = supplemental[SESSION_INFO_KEY.ContextUsage]
  return Object.keys(fields).length ? { ...original, ...fields } : original
}

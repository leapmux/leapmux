import { KIMI_CONVERSATION_FIELD as FIELD, KIMI_CONVERSATION_ORIGIN as ORIGIN } from '~/generated/contracts/kimi-protocol'
import { isObject } from '~/lib/jsonPick'

function optionalStrings(value: Record<string, unknown>, keys: string[]): boolean {
  return keys.every(key => value[key] === undefined || typeof value[key] === 'string')
}

function nonnegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function originFields(value: unknown): boolean {
  if (!isObject(value) || !optionalStrings(value, [
    FIELD.Name,
    FIELD.Path,
    FIELD.Args,
    FIELD.PluginID,
    FIELD.Command,
    FIELD.JobID,
    FIELD.Schedule,
    FIELD.HookEvent,
  ])) {
    return false
  }
  switch (value[FIELD.Kind]) {
    case ORIGIN.User:
    case ORIGIN.Goal:
    case ORIGIN.Retry:
    case ORIGIN.System:
      return true
    case ORIGIN.Skill:
      return typeof value[FIELD.Name] === 'string'
    case ORIGIN.PluginCommand:
      return typeof value[FIELD.PluginID] === 'string' && typeof value[FIELD.Command] === 'string'
    case ORIGIN.Cron:
      return typeof value[FIELD.JobID] === 'string' && typeof value[FIELD.Schedule] === 'string'
        && typeof value[FIELD.Recurring] === 'boolean' && nonnegativeInteger(value[FIELD.CoalescedCount]) && typeof value[FIELD.Stale] === 'boolean'
    case ORIGIN.Hook:
      return typeof value[FIELD.HookEvent] === 'string'
    default:
      return false
  }
}

function attachmentFields(value: unknown): boolean {
  if (!isObject(value))
    return false
  const ordinal = value[FIELD.Ordinal]
  const kind = value[FIELD.Kind]
  return typeof kind === 'string' && ['image', 'video', 'audio', 'file'].includes(kind) && nonnegativeInteger(ordinal) && ordinal > 0
    && optionalStrings(value, [
      FIELD.FileID,
      FIELD.URL,
      FIELD.Name,
      FIELD.MediaType,
    ])
    && (value[FIELD.Size] === undefined || nonnegativeInteger(value[FIELD.Size]))
}

/** Validate the native user metadata before the row enters the neutral model. */
export function kimiConversationUserFields(item: Record<string, unknown>): boolean {
  const attachments = item[FIELD.Attachments]
  const skills = item[FIELD.Skills]
  return (item[FIELD.Placement] === 'opener' || item[FIELD.Placement] === 'steer')
    && originFields(item[FIELD.Origin]) && Array.isArray(attachments) && attachments.every(attachmentFields)
    && Array.isArray(skills) && skills.every(skill => isObject(skill) && typeof skill[FIELD.Name] === 'string' && optionalStrings(skill, [FIELD.Path, FIELD.Args]))
    && (item[FIELD.Metadata] === undefined || isObject(item[FIELD.Metadata]))
}

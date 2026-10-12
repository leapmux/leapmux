import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { GEMINI_SUPPLEMENT, GEMINI_TOOL } from '~/generated/contracts/gemini-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { acpExtractControl, acpPermissionToolCall } from '../acp/extractControl'

const PLAN_TITLE_PREFIX = 'Requesting plan approval for: '

/** Read the native plan file that the exact permission source stores. */
export function geminiExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const tool = acpPermissionToolCall(input.payload)
  const id = pickString(tool, 'toolCallId')
  const title = pickString(tool, 'title')
  if (input.payload.method !== 'session/request_permission' || !id.startsWith(`${GEMINI_TOOL.ExitPlanMode}__`)
    || !title.startsWith(PLAN_TITLE_PREFIX) || title.length === PLAN_TITLE_PREFIX.length) {
    return acpExtractControl(input)
  }
  const source = input.source?.parentObject
  const rawSupplement = input.source?.supplementalContent
  const supplement = isObject(rawSupplement) ? rawSupplement : undefined
  const path = title.slice(PLAN_TITLE_PREFIX.length)
  const ownsSource = pickString(source, 'sessionUpdate') === 'tool_call'
    && pickString(source, 'toolCallId') === id
    && pickString(source, 'title') === title
    && pickString(supplement, GEMINI_SUPPLEMENT.PlanPath) === path
    && pickString(supplement, 'toolCallId') === id
  const text = ownsSource ? pickString(supplement, GEMINI_SUPPLEMENT.PlanContent) : ''
  return { kind: 'plan', ...(text ? { text } : {}) }
}

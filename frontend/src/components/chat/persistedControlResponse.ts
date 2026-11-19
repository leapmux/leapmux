import type { ControlPrompt } from './model/controlPrompt'
import type { ControlResponseSummary } from './model/controlResponse'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { fromJson } from '@bufbuild/protobuf'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { ControlDecisionSchema, ControlPromptSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { controlDecisionSummary } from './controlDecisionSummary'
import { controlPromptFromWire } from './model/controlPromptWire'
import { OPTION_ID_PERMISSION_MODE } from './settingsGroups'

// This module reads the Worker's metadata of a saved control answer, apart from the
// native request and answer beside it. The answer's words come from the prompt and
// the decision that the Worker records there (controlDecisionSummary), so no reader
// of the row reads a provider's wire format.
//
// The transcript row (renderControlResponseRow in messageRenderers) and the scroll
// rail's dot preview (controlResponsePreviewText below) draw the one display that
// layer 1 resolves here, so the dot reads the same as the row that it jumps to.

/** Lead-in shown above the user's typed rejection reason (their feedback follows as markdown). */
export const CONTROL_RESPONSE_FEEDBACK_LEAD = 'Sent feedback:'

/** The label of a row whose prompt and decision state no words. */
const CONTROL_RESPONSE_GENERIC_LABEL = 'Responded'

/** A response resolved from the original message and its separate request context. */
export interface PersistedControlResponse {
  requestId: string
  claimToken: string
  /** Complete native request, when available. No display reads it. */
  request: Record<string, unknown> | undefined
  /** Native response, or undefined when the original bytes are not a JSON object. No display reads it. */
  response: Record<string, unknown> | undefined
  /**
   * What the request asked, as the Worker read it when it recorded the answer, or
   * undefined for a row that states none.
   */
  prompt?: ControlPrompt
  /**
   * The decision that answered the request, as the Worker recorded it, or undefined
   * for a row that states none. The Worker leaves out feedback that the runtime did
   * not receive, and feedback that it delivered as a message of its own.
   */
  decision?: ControlDecision
}

/** Resolve one control response without adding fields to either provider payload. */
export function parsePersistedControlResponse(
  parsed: ParsedMessageContent | null | undefined,
): PersistedControlResponse | null {
  if (!parsed || parsed.wrapper || !isObject(parsed.messageMetadata))
    return null
  const requestId = parsed.messageMetadata[MESSAGE_METADATA_FIELD.ControlRequestID]
  if (typeof requestId !== 'string')
    return null
  const prompt = persistedControlPrompt(parsed.messageMetadata[MESSAGE_METADATA_FIELD.ControlPrompt])
  const decision = persistedControlDecision(parsed.messageMetadata[MESSAGE_METADATA_FIELD.ControlDecision])
  return {
    requestId,
    claimToken: pickString(parsed.messageMetadata, MESSAGE_METADATA_FIELD.ControlRequestClaimToken),
    request: isObject(parsed.supplementalContent)
      ? parsed.supplementalContent
      : undefined,
    response: parsed.parentObject,
    ...(prompt
      ? { prompt }
      : {}),
    ...(decision
      ? { decision }
      : {}),
  }
}

/**
 * The prompt that the Worker recorded with a response row, as protojson, or undefined
 * for a row that states none or a value that does not read as a prompt.
 */
function persistedControlPrompt(value: unknown): ControlPrompt | undefined {
  if (!isObject(value))
    return undefined
  try {
    return controlPromptFromWire(fromJson(ControlPromptSchema, value as Parameters<typeof fromJson>[1], { ignoreUnknownFields: true }))
  }
  catch {
    return undefined
  }
}

/**
 * The decision that the Worker recorded with a response row, as protojson, or
 * undefined for a row that states none, a value that does not read as a decision, or
 * a decision of no kind.
 */
function persistedControlDecision(value: unknown): ControlDecision | undefined {
  if (!isObject(value))
    return undefined
  try {
    const decision = fromJson(ControlDecisionSchema, value as Parameters<typeof fromJson>[1], { ignoreUnknownFields: true })
    return decision.kind.case === undefined
      ? undefined
      : decision
  }
  catch {
    return undefined
  }
}

/**
 * Resolve a persisted control response to its never-null display: the words of its
 * prompt and its decision, or the generic label for a row that states neither, or a
 * decision that does not answer the prompt. The transcript row and the scroll-rail
 * dot both draw this one display.
 *
 * A plan approval names the permission mode that it switches to by the label that
 * `provider`'s settings state for the mode. The read goes through the settings label
 * cache, which records it, so a cached row redraws when the labels arrive.
 */
export function resolveControlResponseSummary(cr: PersistedControlResponse, provider: AgentProvider | undefined): ControlResponseSummary {
  const display = cr.prompt && cr.decision
    ? controlDecisionSummary(cr.prompt, cr.decision, mode => getCachedSettingsLabel(provider, OPTION_ID_PERMISSION_MODE, mode))
    : null
  return display ?? { kind: 'label', text: CONTROL_RESPONSE_GENERIC_LABEL }
}

/**
 * Plaintext projection shared by the rail preview: a label renders verbatim; feedback renders the
 * lead + the reason on the next line (the rail then truncates the whole thing).
 */
export function controlResponsePreviewText(display: ControlResponseSummary): string {
  return display.kind === 'feedback'
    ? `${CONTROL_RESPONSE_FEEDBACK_LEAD}\n${display.message}`
    : display.text
}

import { CONTROL_ANSWER_BEHAVIOR, CONTROL_ANSWER_MESSAGE } from '~/generated/contracts/control-answer'
import { isObject } from '~/lib/jsonPick'

/**
 * The neutral control responses that the Worker sends and stores, for the tests of the
 * displays that read a saved answer. The Worker builds each one in Go
 * (`agent.ControlResponseEnvelope`, `agent.AllowResponse`, `agent.DenyResponse` and
 * `agent.WithControlChoice`); these build the same shapes as fixtures.
 */

/**
 * The control_response envelope, around one provider's own answer.
 *
 * Every neutral answer travels in this envelope, and only the innermost
 * `response` object differs: Claude's SDK defines an allow with an updated input and a
 * deny with a message, Copilot adds an approval SCOPE and a question ANSWER, and an MCP
 * elicitation carries its action and content. The envelope is stated ONCE here, so no
 * fixture can spell `subtype` or `request_id` a second way.
 */
export function buildControlResponseEnvelope(
  requestId: string,
  response: Record<string, unknown>,
): Record<string, unknown> {
  return {
    type: 'control_response',
    response: {
      subtype: 'success',
      request_id: requestId,
      response,
    },
  }
}

/**
 * Builds a control_response JSON object that allows a tool use.
 */
export function buildAllowResponse(
  requestId: string,
  updatedInput: Record<string, unknown>,
): Record<string, unknown> {
  return buildControlResponseEnvelope(requestId, {
    behavior: CONTROL_ANSWER_BEHAVIOR.Allow,
    updatedInput,
  })
}

/**
 * The same control response with the id of one offered choice beside its behavior.
 *
 * A plan approval can offer answers beyond a plain approve and reject
 * (`PlanChoice`), and the choice rides the neutral envelope as `choice`. The worker
 * reads it with `DecodeControlChoice` (backend/internal/worker/agent/control_response.go)
 * and each provider maps the id onto its own answer. The input is not changed.
 */
export function withControlChoice(envelope: Record<string, unknown>, choice: string): Record<string, unknown> {
  const outer = isObject(envelope.response)
    ? envelope.response
    : {}
  const inner = isObject(outer.response)
    ? outer.response
    : {}
  return {
    ...envelope,
    response: {
      ...outer,
      response: { ...inner, choice },
    },
  }
}

/**
 * Builds a control_response JSON object that denies a tool use. Omit `message` for a bare
 * deny with no typed reason (it falls back to CONTROL_ANSWER_MESSAGE.RejectedByUser).
 */
export function buildDenyResponse(
  requestId: string,
  message?: string,
): Record<string, unknown> {
  return buildControlResponseEnvelope(requestId, {
    behavior: CONTROL_ANSWER_BEHAVIOR.Deny,
    // Ensure the message is never empty — Claude Code SDK converts deny
    // responses into tool_result with is_error=true, and the Anthropic API
    // rejects empty content when is_error is set.
    message: message || CONTROL_ANSWER_MESSAGE.RejectedByUser,
  })
}

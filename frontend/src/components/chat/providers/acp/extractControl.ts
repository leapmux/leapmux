import type { PermissionPrompt } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import type { PermissionOption } from '~/components/chat/model/controlPrompt'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { assignDefined, isObject, pickObject, pickString } from '~/lib/jsonPick'
import { resolveACPToolCall } from './extractors/toolCall'

/** The `params` of one `session/request_permission`, or undefined for another shape. */
function acpParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params') ?? undefined
}

/** The tool call a permission request is about, as the request itself states it. */
export function acpPermissionToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(acpParams(payload), 'toolCall') ?? undefined
}

/**
 * The options the runtime offers.
 *
 * Every Agent Client Protocol provider sends its own vocabulary of option ids, and
 * `layoutPermissionOptions` reads the KINDS rather than the ids for exactly that
 * reason. An empty list is a real answer: the shared Allow/Deny pair answers then.
 *
 * READ, never asserted, and every reader of the six providers that share this depends
 * on it. `layoutPermissionOptions` and `permissionOptionLabel` both dereference
 * `option.kind` with no guard, so a `null` element threw the whole banner into the
 * ErrorBoundary. A STRING `options` is quieter and worse: its `length` is the length
 * of the string, so {@link acpPermissionIR} read the payload as a permission request,
 * the layout iterated the CHARACTERS, and the decision row drew one empty button for
 * each of them. An element that is no object cannot state a kind, so it is dropped.
 */
export function acpPermissionOptions(payload: Record<string, unknown>): PermissionOption[] {
  const options = acpParams(payload)?.options
  if (!Array.isArray(options))
    return []
  return options.filter(isObject).map((option) => {
    // An empty name is no name: `permissionOptionLabel` would draw it as the button's
    // words, and its kind fallback is the truthful answer instead.
    const name = pickString(option, 'name')
    return {
      optionId: pickString(option, 'optionId'),
      kind: pickString(option, 'kind'),
      ...(name !== '' ? { name } : {}),
    }
  })
}

/**
 * One Agent Client Protocol permission request, read into the shared model.
 *
 * The request states its tool call in a COMPACT form -- an id, a title and a kind --
 * and the arguments live on the tool-request row of the same call. `resolveACPToolCall`
 * merges the two, which is why this takes the paired request: without it a permission
 * banner showed a title and no arguments at all.
 *
 * Returns null for a payload that is no permission request at all.
 */
export function acpPermissionIR(input: ControlExtractionInput): PermissionPrompt | null {
  const original = acpPermissionToolCall(input.payload)
  const options = acpPermissionOptions(input.payload)
  // A permission request states a tool call, an option list, or both. A payload with
  // NEITHER is some other request, and the caller draws it as the generic row. The
  // option list alone is enough on its own: it is what the decision buttons send, and
  // a request read as a non-permission would answer it with the wrong envelope.
  if (!original && options.length === 0)
    return null
  const toolCall: Record<string, unknown> = original ? resolveACPToolCall(original, input.request?.parentObject) : {}
  const kind = pickString(toolCall, 'kind')
  const permission: PermissionPrompt = {
    title: pickString(toolCall, 'title') || kind,
    input: toolCall[ACP_SUPPLEMENT_REQUEST.RawInput],
    options,
  }
  // A shell call states its command, which the banner draws as code above the
  // arguments. Every other kind states none, and the arguments carry the whole ask.
  if (kind === 'execute') {
    assignDefined(
      permission,
      'command',
      pickString(pickObject(toolCall, ACP_SUPPLEMENT_REQUEST.RawInput), 'command', undefined),
    )
  }
  return permission
}

/**
 * `Provider.extractControl` for every Agent Client Protocol provider.
 *
 * OpenCode, Kilo, Goose and Reasonix share it whole. Cursor delegates to it for the
 * requests it does not answer itself, exactly as its control component did.
 */
export function acpExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const permission = acpPermissionIR(input)
  return permission ? { kind: 'permission', permission } : null
}

/**
 * The tool call a permission request identifies, for the caller that loads its row.
 *
 * The banner keeps that row loaded for as long as the request lives, and the id is
 * the only thing it needs to ask for one. It lives here rather than in the banner so
 * the wire path `params.toolCall.toolCallId` stays inside the provider layer.
 */
export function acpPermissionSpanId(payload: Record<string, unknown>): string {
  return pickString(acpPermissionToolCall(payload), 'toolCallId')
}

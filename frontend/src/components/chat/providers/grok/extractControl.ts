import type { ElicitationRequest, PermissionOption } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { GROK_METHOD, GROK_TRUST_OUTCOME } from '~/generated/contracts/grok-protocol'
import { pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { KIND_ALLOW_ONCE, KIND_REJECT_ONCE } from '../../model/controlPrompt'
import { acpElicitation } from '../acp/elicitation'
import { acpExtractControl } from '../acp/extractControl'

/**
 * The two answers to Grok's folder-trust question, as the decision row draws them.
 *
 * Grok sends no option list for this request, so LeapMux states one. The option ids
 * ARE Grok's own two reply words, which is what lets the worker read the reader's
 * choice back without a table of its own. Grok keeps the answer for the whole
 * workspace, so the words say that.
 */
export const GROK_FOLDER_TRUST_OPTIONS: readonly PermissionOption[] = [
  { optionId: GROK_TRUST_OUTCOME.Trust, kind: KIND_ALLOW_ONCE, name: 'Trust this workspace' },
  { optionId: GROK_TRUST_OUTCOME.Reject, kind: KIND_REJECT_ONCE, name: 'Do not trust' },
]

/** The words each reply word of the folder-trust question carried as a button. */
export function grokFolderTrustLabel(outcome: string): string | undefined {
  return GROK_FOLDER_TRUST_OPTIONS.find(option => option.optionId === outcome)?.name
}

/**
 * `Provider.extractControl` for Grok Build.
 *
 * Grok raises two of its own requests that this reader draws: a plan approval and
 * the folder-trust question. Its questions and its MCP forms take their own surfaces
 * before this runs, and every tool permission is the standard Agent Client Protocol
 * request, so the rest DELEGATES to the shared reader.
 */
export function grokExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const { payload } = input
  const params = pickObject(payload, 'params')
  if (payload.method === GROK_METHOD.ExitPlanMode) {
    // Grok sends the plan's markdown, or `null` for an empty plan. The approval then
    // draws no plan text, which is what the reader would see in the plan file too.
    const text = pickString(params, 'planContent')
    return { kind: 'plan', ...(text ? { text } : {}) }
  }
  if (payload.method === GROK_METHOD.FolderTrust) {
    const kinds = stringArray(params?.configKinds).filter(Boolean)
    const workspace = pickString(params, 'workspace') || pickString(params, 'cwd')
    const found = kinds.length > 0 ? ` (${kinds.join(', ')})` : ''
    return {
      kind: 'permission',
      permission: {
        title: workspace ? `Trust the workspace ${workspace}` : 'Trust this workspace',
        reason: `The repository holds its own Grok Build configuration${found}. Grok Build loads it only from a workspace you trust, and it keeps your answer for the workspace.`,
        options: [...GROK_FOLDER_TRUST_OPTIONS],
      },
    }
  }
  return acpExtractControl(input)
}

/**
 * The MCP form or URL one Grok request asks for.
 *
 * Grok raises an MCP elicitation under its own method, with the server under
 * `serverName`. The standard `elicitation/create` still reads through the shared
 * reader, so a later Grok that sends it draws the same form.
 */
export function grokElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== GROK_METHOD.McpElicit)
    return acpElicitation(payload)
  const params = pickObject(payload, 'params') ?? {}
  return {
    mode: pickString(params, 'mode', 'form'),
    message: pickString(params, 'message'),
    server: pickString(params, 'serverName'),
    schema: params.requestedSchema,
    url: pickString(params, 'url'),
    title: '',
    description: '',
  }
}

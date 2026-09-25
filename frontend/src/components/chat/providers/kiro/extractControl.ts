import type { ElicitationRequest, PermissionOption, PermissionScope } from '../../model/controlPrompt'
import type { ControlExtractionInput, ExtractedControlRequest } from '../registry'
import { KIRO_META, KIRO_METHOD, KIRO_PERMISSION_OPTION, KIRO_SCOPED_PERMISSION_OPTION } from '~/generated/contracts/kiro-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { KIND_ALLOW_ALWAYS, KIND_REJECT_ALWAYS } from '../../model/controlPrompt'
import { acpElicitation } from '../acp/elicitation'
import { acpExtractControl } from '../acp/extractControl'
import { KIRO_TURN_APPROVAL, kiroMeta } from './protocol'

/** One always option of the decision row: the name that it shows and the scope of its rule. */
interface KiroAlwaysRule {
  name: string
  scope: PermissionScope
}

/**
 * Each always-allow option, by its id, Kiro's own first.
 *
 * Kiro states one `always-accept`, named `Always allow`, which keeps its rule for the
 * session, and reads a wider scope from the reply. LeapMux states the two wider
 * scopes as options of their own, which the worker turns back into Kiro's
 * `always-accept` with the scope. LeapMux writes these options, so it states the
 * scope of each one, and the scope pills of the decision row read `Session`,
 * `Workspace` and `Always` whatever the names say.
 */
export const KIRO_ALWAYS_ALLOW = {
  [KIRO_PERMISSION_OPTION.AlwaysAccept]: { name: 'Always allow for this session', scope: 'session' },
  [KIRO_SCOPED_PERMISSION_OPTION.AlwaysAcceptWorkspace]: { name: 'Always allow in this workspace', scope: 'workspace' },
  [KIRO_SCOPED_PERMISSION_OPTION.AlwaysAcceptUser]: { name: 'Always allow everywhere', scope: 'user' },
} as const satisfies Record<string, KiroAlwaysRule>

/**
 * Each always-deny option, by its id, Kiro's own first.
 *
 * Kiro's `always-reject` reads the scope of the reply as its `always-accept` does, so
 * LeapMux states the same two wider scopes for it. The worker turns each back into
 * Kiro's `always-reject` with the scope. Deny sends the option of the scope that the
 * selected pill states.
 */
export const KIRO_ALWAYS_DENY = {
  [KIRO_PERMISSION_OPTION.AlwaysReject]: { name: 'Always deny for this session', scope: 'session' },
  [KIRO_SCOPED_PERMISSION_OPTION.AlwaysRejectWorkspace]: { name: 'Always deny in this workspace', scope: 'workspace' },
  [KIRO_SCOPED_PERMISSION_OPTION.AlwaysRejectUser]: { name: 'Always deny everywhere', scope: 'user' },
} as const satisfies Record<string, KiroAlwaysRule>

/** The id of one always-allow option. */
export type KiroAlwaysAllowID = keyof typeof KIRO_ALWAYS_ALLOW

/** The id of one always-deny option. */
export type KiroAlwaysDenyID = keyof typeof KIRO_ALWAYS_DENY

/**
 * The options of one always rule, one for each scope that the request can keep.
 *
 * Kiro's own option keeps its fields and takes the name and the scope of the table.
 * A wider scope is left out when `wider` is false, and the workspace scope is left
 * out when the request states no workspace root.
 */
function kiroAlwaysRuleOptions(own: PermissionOption, rules: Readonly<Record<string, KiroAlwaysRule>>, scopes: { wider: boolean, workspace: boolean }): PermissionOption[] {
  return Object.entries(rules).flatMap(([optionId, { name, scope }]): PermissionOption[] => {
    if (optionId === own.optionId)
      return [{ ...own, name, scope }]
    if (!scopes.wider || (scope === 'workspace' && !scopes.workspace))
      return []
    return [{ optionId, kind: own.kind, name, scope }]
  })
}

/**
 * The options of one permission request, with the wider scopes of each always option.
 *
 * - Kiro offers `always-accept` only for an implicit ask whose rule can persist, and
 *   `always-reject` for each ask whose rule can persist.
 * - The wider scopes ride the scope pills of the decision row, and `always-accept`
 *   is what draws the pills. With no `always-accept`, the always-deny keeps the
 *   session alone: each wider scope would be one more button.
 * - A rule for the workspace needs the workspace root, which the request states in
 *   its consent. With no root, the workspace options stay out, because Kiro would
 *   refuse them.
 */
export function kiroPermissionOptions(options: PermissionOption[], meta: Record<string, unknown> | undefined): PermissionOption[] {
  const allow = options.find(option => option.optionId === KIRO_PERMISSION_OPTION.AlwaysAccept && option.kind === KIND_ALLOW_ALWAYS)
  const deny = options.find(option => option.optionId === KIRO_PERMISSION_OPTION.AlwaysReject && option.kind === KIND_REJECT_ALWAYS)
  if (!allow && !deny)
    return options
  const scopes = {
    wider: allow !== undefined,
    workspace: pickString(pickObject(meta, KIRO_META.Consent), 'workspaceRoot').trim() !== '',
  }
  return options.flatMap((option) => {
    if (option === allow)
      return kiroAlwaysRuleOptions(option, KIRO_ALWAYS_ALLOW, scopes)
    if (option === deny)
      return kiroAlwaysRuleOptions(option, KIRO_ALWAYS_DENY, scopes)
    return [option]
  })
}

/** The files of one turn review, one line each. */
function turnApprovalFiles(meta: Record<string, unknown> | undefined): string[] {
  const files = meta?.files
  if (!Array.isArray(files))
    return []
  return files.filter(isObject).map(file => pickString(file, 'path')).filter(Boolean)
}

/**
 * `Provider.extractControl` for Kiro.
 *
 * Kiro raises every tool permission, and the review of a Supervised turn, as the
 * standard Agent Client Protocol request, so this reads them through the shared
 * reader and adds what only Kiro states: the wider scopes of an always-allow, the
 * command of a shell call, and the files of a turn review. Kiro's question and MCP
 * form take their own surfaces before this runs.
 */
export function kiroExtractControl(input: ControlExtractionInput): ExtractedControlRequest | null {
  const shared = acpExtractControl(input)
  if (!shared || shared.kind !== 'permission')
    return shared
  const meta = kiroMeta(pickObject(input.payload, 'params'))
  const permission = { ...shared.permission, options: kiroPermissionOptions(shared.permission.options, meta) }
  if (pickString(meta, 'type') === KIRO_TURN_APPROVAL) {
    const files = turnApprovalFiles(meta)
    return {
      kind: 'permission',
      permission: {
        ...permission,
        reason: files.length > 0
          ? `Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file:\n${files.map(file => `- ${file}`).join('\n')}`
          : 'Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file.',
      },
    }
  }
  const command = pickString(meta, 'command')
  return { kind: 'permission', permission: command && !permission.command ? { ...permission, command } : permission }
}

/**
 * The MCP form or URL one Kiro request asks for.
 *
 * Kiro raises an MCP elicitation under its own method, with MCP's own request under
 * `elicitation`. The standard `elicitation/create` still reads through the shared
 * reader, so a later Kiro that sends it draws the same form.
 */
export function kiroElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== KIRO_METHOD.McpElicitation)
    return acpElicitation(payload)
  const elicitation = pickObject(pickObject(payload, 'params'), 'elicitation') ?? {}
  return {
    mode: pickString(elicitation, 'mode', 'form'),
    message: pickString(elicitation, 'message'),
    server: '',
    schema: elicitation.requestedSchema,
    url: pickString(elicitation, 'url'),
    title: '',
    description: '',
  }
}

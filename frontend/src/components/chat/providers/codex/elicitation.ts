import type { ElicitationRequest } from '../../controls/elicitationForm'
import { MCP_ELICITATION_APPROVAL_KIND, MCP_ELICITATION_APPROVAL_SCOPE, MCP_ELICITATION_METHOD } from '~/generated/contracts/mcp-elicitation'
import { pickObject, pickString } from '~/lib/jsonPick'

export function codexElicitation(payload: Record<string, unknown>): ElicitationRequest | undefined {
  if (payload.method !== MCP_ELICITATION_METHOD.Codex)
    return undefined
  const params: Record<string, unknown> = pickObject(payload, 'params', {})
  const mode = pickString(params, 'mode', 'form')
  const meta: Record<string, unknown> = pickObject(params, '_meta', {})
  const approval = meta.codex_approval_kind === MCP_ELICITATION_APPROVAL_KIND.ToolCall || meta.codex_approval_kind === MCP_ELICITATION_APPROVAL_KIND.ToolSuggestion
  const persist = Array.isArray(meta.persist) ? meta.persist : []
  const acceptChoices: NonNullable<ElicitationRequest['acceptChoices']> = [{ key: 'once', label: 'Once' }]
  if (approval) {
    if (persist.includes(MCP_ELICITATION_APPROVAL_SCOPE.Session))
      acceptChoices.push({ key: MCP_ELICITATION_APPROVAL_SCOPE.Session, label: 'Session', metadata: { persist: MCP_ELICITATION_APPROVAL_SCOPE.Session } })
    if (persist.includes(MCP_ELICITATION_APPROVAL_SCOPE.Always))
      acceptChoices.push({ key: MCP_ELICITATION_APPROVAL_SCOPE.Always, label: 'Always', metadata: { persist: MCP_ELICITATION_APPROVAL_SCOPE.Always } })
  }
  return {
    mode: mode === 'openai/form' || mode === 'openaiForm' ? 'form' : mode,
    message: pickString(params, 'message', ''),
    server: pickString(params, 'serverName', ''),
    schema: params.requestedSchema,
    url: pickString(params, 'url', ''),
    title: pickString(params, 'title', approval ? 'Permission Required' : ''),
    arguments: approval ? meta.tool_params : undefined,
    acceptChoices: approval ? acceptChoices : undefined,
    description: pickString(params, 'description', approval ? pickString(meta, 'tool_description', '') : ''),
  }
}

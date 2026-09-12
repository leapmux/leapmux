import type { ElicitationRequest } from '../../controls/elicitationForm'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { MCP_ELICITATION_APPROVAL_SCOPE } from '~/generated/contracts/mcp-elicitation'
import { PI_DIALOG_METHOD, PI_EVENT, PI_MCP_APPROVAL_CHOICE, PI_MCP_APPROVAL_TEXT } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { stripAnsi } from '~/lib/renderAnsi'
import { PI_MCP_TOOL } from './protocol'

/** Recognize the installed MCP adapter's permission dialog, including its exact choices. */
export function isPiMcpApproval(payload: Record<string, unknown>): boolean {
  const title = pickString(payload, 'title')
  const options = payload.options
  return payload.type === PI_EVENT.ExtensionUIRequest && payload.method === PI_DIALOG_METHOD.Select
    && title.startsWith(PI_MCP_APPROVAL_TEXT.TitlePrefix) && title.includes(PI_MCP_APPROVAL_TEXT.ArgumentsMarker)
    && Array.isArray(options) && options.length === 3
    && options[0] === PI_MCP_APPROVAL_CHOICE.AllowOnce && options[1] === PI_MCP_APPROVAL_CHOICE.AllowForSession && options[2] === PI_MCP_APPROVAL_CHOICE.Deny
}

/** Compare against the adapter's flattened preview without changing the displayed arguments. */
function matchesPreview(args: Record<string, unknown>, preview: string): boolean {
  const text = stripAnsi(JSON.stringify(args, null, 2)).replace(/\p{Cc}+/gu, ' ').replace(/\s+/gu, ' ').trim()
  return text === preview || (preview.endsWith('...') && preview.length > 3 && text.startsWith(preview.slice(0, -3)))
}

function argumentObject(value: unknown): Record<string, unknown> | undefined {
  if (isObject(value))
    return value
  if (typeof value !== 'string')
    return undefined
  try {
    const parsed: unknown = JSON.parse(value)
    return isObject(parsed) ? parsed : undefined
  }
  catch {
    return undefined
  }
}

export function piMcpApproval(payload: Record<string, unknown>, source?: ParsedMessageContent): ElicitationRequest | undefined {
  if (!isPiMcpApproval(payload))
    return undefined
  const title = pickString(payload, 'title')
  const marker = title.indexOf(PI_MCP_APPROVAL_TEXT.ArgumentsMarker)
  const preview = title.slice(marker + PI_MCP_APPROVAL_TEXT.ArgumentsMarker.length)
  const original = source?.parentObject
  const input = pickObject(original, 'args') ?? pickObject(original, 'input')
  const args = original?.toolName === PI_MCP_TOOL.Gateway ? argumentObject(input?.args) : input
  const recovered = original?.type === PI_EVENT.ToolExecutionStart && isObject(args) && matchesPreview(args, preview) ? args : undefined
  return {
    mode: 'form',
    title: 'Permission Required',
    message: title.slice(0, marker),
    schema: { type: 'object', properties: {} },
    arguments: recovered ?? preview,
    argumentNotice: !recovered && preview.endsWith('...') ? 'Pi truncated the argument preview.' : undefined,
    acceptChoices: [
      { key: 'once', label: 'Once' },
      { key: MCP_ELICITATION_APPROVAL_SCOPE.Session, label: 'Session', metadata: { persist: MCP_ELICITATION_APPROVAL_SCOPE.Session } },
    ],
  }
}

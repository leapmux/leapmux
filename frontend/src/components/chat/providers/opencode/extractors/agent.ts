import type { AgentRun } from '../../../model/tools/agent'
import { pickObject, pickString } from '~/lib/jsonPick'

/** Read only the complete wrapper that the native task tool writes. Keep the report's text intact. */
export function openCodeTaskResult(output: string, metadata: Record<string, unknown> | null, input: Record<string, unknown>): AgentRun | null {
  const headerEnd = output.indexOf('\n')
  if (headerEnd < 0)
    return null
  const match = /^<task id="([^"\r\n]+)" state="(running|completed|error)">$/.exec(output.slice(0, headerEnd))
  if (!match)
    return null
  // Both groups are pinned by the pattern above; the fallbacks are the type-level guard alone.
  const agentId = match[1] ?? ''
  const state = match[2] ?? ''
  const storedId = pickString(metadata, 'sessionId')
  if (storedId && storedId !== agentId)
    return null
  let bodyStart = headerEnd + 1
  if (output.startsWith('<summary>', bodyStart)) {
    const summaryEnd = output.indexOf('</summary>\n', bodyStart)
    if (summaryEnd < 0)
      return null
    bodyStart = summaryEnd + '</summary>\n'.length
  }
  const tag = state === 'error' ? 'task_error' : 'task_result'
  const opening = `<${tag}>\n`
  const closing = `\n</${tag}>\n</task>`
  if (!output.startsWith(opening, bodyStart) || !output.endsWith(closing))
    return null
  bodyStart += opening.length
  const bodyEnd = output.length - closing.length
  if (bodyEnd < bodyStart)
    return null
  const report = output.slice(bodyStart, bodyEnd)
  const model = pickString(pickObject(metadata, 'model'), 'modelID')
  const rows: AgentRun['metadata'] = [{ label: 'Agent ID', value: agentId }]
  if (model)
    rows.push({ label: 'Model', value: model })
  const bodyLabel = state === 'running' ? 'Prompt' : undefined
  return {
    description: pickString(input, 'description').trim(),
    agentId,
    statusLabel: state === 'running' ? 'launched asynchronously' : state === 'error' ? 'failed' : 'completed',
    outcome: state === 'error' ? 'failed' : state === 'running' ? 'running' : 'completed',
    metadata: rows,
    body: state === 'running' ? pickString(input, 'prompt') : report,
    ...(bodyLabel !== undefined ? { bodyLabel } : {}),
  }
}

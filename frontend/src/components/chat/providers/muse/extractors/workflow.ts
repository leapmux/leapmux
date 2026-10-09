import type { ToolCall, ToolResult } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { MUSE_ITEM_KIND } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult } from '../../../model/toolCall'
import { MUSE_WORKFLOW_RECONCILIATION, museItem } from '../protocol'
import { MUSE_TOOL } from '../toolNames'
import { museItemLifecycle } from './toolCommon'

function readReconciliation(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== 'string'
    || !value.startsWith(MUSE_WORKFLOW_RECONCILIATION.Open)
    || !value.endsWith(MUSE_WORKFLOW_RECONCILIATION.Close)) {
    return undefined
  }
  const body = value.slice(MUSE_WORKFLOW_RECONCILIATION.Open.length, -MUSE_WORKFLOW_RECONCILIATION.Close.length)
  try {
    const parsed: unknown = JSON.parse(body)
    if (!isObject(parsed)
      || parsed.type !== MUSE_WORKFLOW_RECONCILIATION.Type
      || typeof parsed.call_id !== 'string' || parsed.call_id.trim() === ''
      || typeof parsed.launch_command_id !== 'string' || parsed.launch_command_id.trim() === '') {
      return undefined
    }
    return parsed
  }
  catch {
    return undefined
  }
}

/** Read the computed workflow result without reusing its separate launch receipt. */
export function museWorkflowResult(input: RowExtractionInput): ToolCall<'other'> | null {
  const item = museItem(input.resolved.parentObject)
  if (item?.kind !== MUSE_ITEM_KIND.Workflow)
    return null
  const { facts } = museItemLifecycle(item.status, input.completion ?? input.resolved.completion)
  const reconciled = readReconciliation(item.message)
  let result: ToolResult<'other'> | undefined
  if (facts.providerOutcome === 'succeeded') {
    const summary = pickObject(reconciled, 'final_summary')
    if (summary?.status === 'completed' && typeof summary.summary === 'string')
      result = { content: [{ type: 'text', text: summary.summary }] }
  }
  else if (facts.providerOutcome === 'failed' || facts.providerOutcome === 'declined') {
    const failure = pickObject(reconciled, 'latest_failure')
    if (typeof failure?.error === 'string' && failure.error.trim() !== '')
      result = failedResult(failure.error)
  }
  const title = pickString(item, 'entryId')
  return createToolCall({ id: pickString(item, 'itemId'), name: MUSE_TOOL.Workflow, lifecycle: facts }, {
    kind: 'other',
    request: { args: {} },
    ...(title ? { title } : {}),
    ...(result !== undefined ? { result } : {}),
  })
}

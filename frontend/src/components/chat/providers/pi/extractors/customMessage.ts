import type { AgentResultSource } from '../../../results/agentResult'
import { PI_CUSTOM_TYPE, PI_EVENT } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration, formatNumber } from '../../../rendererUtils'
import { piContentText } from '../messageContent'

/** Pi custom messages can carry plain text or content blocks. */
export function piVisibleCustomMessage(payload: Record<string, unknown>): Record<string, unknown> | null {
  const message = pickObject(payload, 'message')
  return payload.type === PI_EVENT.MessageEnd && message?.role === 'custom' && message.display !== false ? message : null
}

/** Match each XML report to its structured child ID. Invalid or duplicate records cannot replace the preview. */
function notificationReports(content: string): Map<string, string> {
  const reports = new Map<string, string>()
  const duplicateIds = new Set<string>()
  for (const match of content.matchAll(/<task-notification>[\s\S]*?<\/task-notification>/g)) {
    const document = new DOMParser().parseFromString(match[0], 'application/xml')
    if (document.querySelector('parsererror'))
      continue
    const root = document.documentElement
    const ids = [...root.children].filter(child => child.tagName === 'task-id')
    const results = [...root.children].filter(child => child.tagName === 'result')
    if (ids.length !== 1 || results.length !== 1 || ids[0].children.length || results[0].children.length)
      continue
    const id = ids[0].textContent ?? ''
    if (!id || duplicateIds.has(id))
      continue
    if (reports.has(id)) {
      reports.delete(id)
      duplicateIds.add(id)
      continue
    }
    reports.set(id, results[0].textContent ?? '')
  }
  return reports
}

/** Adapt individual and grouped child completions to the shared agent result component. */
function extractSubagentNotificationSources(payload: Record<string, unknown>): AgentResultSource[] | null {
  const message = piVisibleCustomMessage(payload)
  if (message?.customType !== PI_CUSTOM_TYPE.SubagentNotification)
    return null
  const details = pickObject(message, 'details')
  if (!details)
    return null
  const reports = notificationReports(piContentText(payload, 'text'))
  const entries = [details, ...(Array.isArray(details.others) ? details.others : [])]
  const sources: AgentResultSource[] = []
  for (const entry of entries) {
    if (!isObject(entry))
      continue
    const id = pickString(entry, 'id')
    const status = pickString(entry, 'status')
    if (!id || !status)
      continue
    const metadata: AgentResultSource['metadata'] = [{ label: 'Agent ID', value: id }]
    for (const [key, label] of [['toolUses', 'Tool uses'], ['turnCount', 'Turns'], ['maxTurns', 'Maximum turns'], ['totalTokens', 'Tokens'], ['durationMs', 'Duration']]) {
      const value = entry[key]
      if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0)
        metadata.push({ label, value: key === 'durationMs' ? formatDuration(value) : formatNumber(value) })
    }
    for (const [key, label] of [['outputFile', 'Transcript'], ['error', 'Error']]) {
      const value = pickString(entry, key)
      if (value)
        metadata.push({ label, value })
    }
    sources.push({
      description: pickString(entry, 'description').trim(),
      agentId: id,
      registryKey: id,
      status: status === 'error' ? 'failed' : status === 'aborted' || status === 'steered' ? 'partial' : status,
      outcome: status === 'completed' ? 'completed' : status === 'error' ? 'failed' : status === 'stopped' ? 'stopped' : status === 'running' || status === 'queued' ? 'running' : 'unknown',
      metadata,
      body: reports.get(id) ?? pickString(entry, 'resultPreview'),
    })
  }
  return sources.length ? sources : null
}

const notificationCache = new WeakMap<Record<string, unknown>, AgentResultSource[] | null>()

/** Classification, rendering, and copying share one parse of each immutable message. */
export function piSubagentNotificationSources(payload: Record<string, unknown>): AgentResultSource[] | null {
  const cached = notificationCache.get(payload)
  if (cached !== undefined)
    return cached
  const sources = extractSubagentNotificationSources(payload)
  notificationCache.set(payload, sources)
  return sources
}

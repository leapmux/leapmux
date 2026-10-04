import type { ModelRequestContext } from './mockModelRequest'
import type { MockModelProtocol, MockModelRateLimits, MockModelStep } from './mockModelScript'
import { applyClaudeChildHandback, claudeDeliveredChildResponse } from './claudeChildResponse'

/** Recognize only the native delivered-child continuation before ordered script selection. */
export function claudeLifecycleAnswer(context: ModelRequestContext): { rule: string, step: MockModelStep } | undefined {
  const step = context.protocol === 'anthropic-messages' ? claudeDeliveredChildResponse(context.body) : undefined
  return step ? { rule: 'claude-child-handback-complete', step } : undefined
}

/** Adapt an exact native child report without changing another model protocol. */
export function prepareClaudeMessageStep(protocol: MockModelProtocol, body: unknown, step: MockModelStep, id: string): MockModelStep {
  return protocol === 'anthropic-messages' ? applyClaudeChildHandback(body, step, id) : step
}

/** Encode the quota fields that the installed Claude service client reads. */
export function claudeRateLimitHeaders(rateLimits: MockModelRateLimits): Record<string, string> {
  const headers: Record<string, string> = {
    'anthropic-ratelimit-unified-status': rateLimits.status,
    'anthropic-ratelimit-unified-representative-claim': rateLimits.type,
  }
  const abbrev = rateLimits.type.startsWith('seven_day') ? '7d' : '5h'
  if (rateLimits.utilization !== undefined)
    headers[`anthropic-ratelimit-unified-${abbrev}-utilization`] = String(rateLimits.utilization)
  if (rateLimits.resetsAt !== undefined) {
    headers[`anthropic-ratelimit-unified-${abbrev}-reset`] = String(rateLimits.resetsAt)
    headers['anthropic-ratelimit-unified-reset'] = String(rateLimits.resetsAt)
  }
  if (rateLimits.status === 'allowed_warning' && rateLimits.utilization !== undefined)
    headers[`anthropic-ratelimit-unified-${abbrev}-surpassed-threshold`] = String(rateLimits.utilization)
  return headers
}

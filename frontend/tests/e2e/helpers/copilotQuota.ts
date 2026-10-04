import type { MockModelRateLimits } from './mockModelScript'

const QUOTA_RESOURCES = new Set(['premium_interactions', 'chat', 'completions'])

/** Encode the native Copilot quota header with a fixed mock entitlement of 100 requests. */
export function copilotQuotaHeaders(rateLimits: MockModelRateLimits): Record<string, string> {
  if (!QUOTA_RESOURCES.has(rateLimits.type))
    return {}
  const utilization = rateLimits.utilization ?? 0
  if (!Number.isFinite(utilization) || utilization < 0 || utilization > 1)
    throw new Error('The native Copilot quota utilization must be between zero and one.')
  const remaining = Math.round(100 * (1 - utilization))
  const snapshot = new URLSearchParams({ ent: '100', rem: String(remaining), ov: '0', ovPerm: 'false' })
  const resetDate = rateLimits.resetsAt === undefined ? '' : `&rst=${new Date(rateLimits.resetsAt * 1000).toISOString()}`
  return { [`x-quota-snapshot-${rateLimits.type}`]: `${snapshot}${resetDate}` }
}

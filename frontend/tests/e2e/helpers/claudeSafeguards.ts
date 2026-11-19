import type { MockModelStep } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

/** Encode the server safety verdicts that Claude reads from an Anthropic reply. */
export function claudeSafeguardResults(body: unknown, step: Pick<MockModelStep, 'toolCalls' | 'toolSafety'>): Record<string, unknown>[] | undefined {
  const requested = isObject(body) && Array.isArray(body.safeguards)
    && body.safeguards.some(value => isObject(value) && value.type === 'dangerous_tool_use')
  if (!requested) {
    if (step.toolSafety !== undefined)
      throw new Error('The scripted tool safety check requires Anthropic dangerous_tool_use safeguards.')
    return undefined
  }
  const calls = step.toolCalls ?? []
  if (new Set(calls.map(call => call.id)).size !== calls.length)
    throw new Error('The Claude safety check requires unique tool call IDs.')
  return [
    {
      type: 'dangerous_tool_use',
      status: {
        type: 'available',
        tool_uses: Object.fromEntries(calls.map((call) => {
          const decision = step.toolSafety?.decisions && Object.hasOwn(step.toolSafety.decisions, call.id)
            ? step.toolSafety.decisions[call.id]
            : undefined
          return [
            call.id,
            {
              type: 'evaluated',
              outcome: decision?.decision === 'deny'
                ? 'flagged'
                : 'not_flagged',
              ...(decision?.reason === undefined
                ? {}
                : { explanation: decision.reason }),
            },
          ]
        })),
      },
    },
  ]
}

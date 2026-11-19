import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { reasonixControlResponseSummary } from './controlResponse'

function record(request: Record<string, unknown> | undefined, optionId: string, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'jsonrpc:3',
    claimToken: 'claim',
    request,
    response: {
      result: {
        outcome: { outcome: 'selected', optionId },
      },
    },
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** The prompt that the Worker records for the plan gate. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }

const REQUEST = {
  method: 'session/request_permission',
  params: {
    toolCall: {
      title: 'exit_plan_mode',
      _meta: {
        'reasonix.io': { tool: 'exit_plan_mode' },
      },
    },
    options: [
      { optionId: 'allow_once', name: 'Allow', kind: 'allow_once' },
      { optionId: 'reject_once', name: 'Reject', kind: 'reject_once' },
    ],
  },
}

describe('reasonixControlResponseSummary', () => {
  it.each([
    ['allow_once', 'Approve'],
    ['reject_once', 'Reject'],
  ])('shows the plan decision for %s', (optionId, text) => {
    expect(reasonixControlResponseSummary(record(REQUEST, optionId, PLAN_PROMPT))).toEqual({ kind: 'label', text })
  })

  it('keeps an unknown native option readable', () => {
    expect(reasonixControlResponseSummary(record(REQUEST, 'future_choice', PLAN_PROMPT))).toEqual({ kind: 'label', text: 'future_choice' })
  })

  // The kind comes from the prompt alone: a row that recorded none is no plan, and its
  // option reads in the request's own words.
  it('shows the option of a plan gate whose row recorded no prompt', () => {
    expect(reasonixControlResponseSummary(record(REQUEST, 'allow_once'))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // The prompt states the plan apart from the request, and Reasonix's option ids are
  // the protocol's kind tokens, so a plan gate whose request is gone still reads in the
  // plan words.
  it.each([
    ['allow_once', 'Approve'],
    ['reject_once', 'Reject'],
  ])('shows the plan decision for %s when the request is gone', (optionId, text) => {
    expect(reasonixControlResponseSummary(record(undefined, optionId, PLAN_PROMPT))).toEqual({ kind: 'label', text })
  })

  it('delegates ordinary permissions', () => {
    const request = {
      ...REQUEST,
      params: {
        ...REQUEST.params,
        toolCall: {
          title: 'bash',
          _meta: {
            'reasonix.io': { tool: 'bash' },
          },
        },
      },
    }
    // The permission prompt that the Worker records holds the request's own options.
    const prompt = {
      kind: 'permission',
      permission: {
        options: REQUEST.params.options.map(option => ({ ...option })),
      },
    } as const
    expect(reasonixControlResponseSummary(record(request, 'allow_once', prompt))).toEqual({ kind: 'label', text: 'Allow' })
  })
})

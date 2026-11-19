import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { CONTROL_ANSWER_MESSAGE } from '~/generated/contracts/control-answer'
import { QODER_MODE } from '~/generated/contracts/qoder-protocol'
import { GENERIC_PERMISSION_PROMPT } from '~/test-support/controlRequests'
import { } from '~/test-support/controlResponses'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { qoderControls } from './pluginControls'

/** A stored can_use_tool request in the shape the worker publishes it. */
function approvalPayload(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_request',
    request_id: 'approval:ap1',
    request: { tool_name: toolName, tool_use_id: 'call-1', input },
  }
}

/** The prompt that the Worker records for `ExitPlanMode`: the plan approval. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }

/**
 * The decision object that the Worker sends to Qoder and stores as the answer row
 * (`translateQoderCanUseTool`): Qoder's own fields inside the control-response envelope.
 */
function storedAnswer(answer: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_response',
    response: { subtype: 'success', request_id: 'approval:ap1', response: answer },
  }
}

/** One saved row: the stored answer, the native request, and the prompt that the Worker recorded. */
function saved(request: Record<string, unknown>, prompt: ControlPrompt, answer: Record<string, unknown>): PersistedControlResponse {
  return {
    requestId: 'approval:ap1',
    claimToken: 'claim',
    request,
    response: storedAnswer(answer),
    prompt,
  }
}

/** What the transcript row and the scroll-rail dot both show for one saved row. */
function display(cr: PersistedControlResponse) {
  return resolveControlResponseSummary(cr, qoderControls.controlResponseDisplay)
}

const BASH = approvalPayload('Bash', { command: 'ls' })
const EXIT_PLAN = approvalPayload('ExitPlanMode', { plan: 'Do the thing.' })

describe('qoderControls', () => {
  // The Worker reads each request and builds each answer, so the browser keeps only the
  // display of a saved answer and the presets.
  it('registers only the saved-answer display and the presets', () => {
    expect(Object.keys(qoderControls).sort()).toEqual(['controlResponseDisplay', 'permissionPresets'])
  })
})

describe('qoderControls controlResponseDisplay', () => {
  it('reads a permission answer with the words of its buttons', () => {
    expect(display(saved(BASH, GENERIC_PERMISSION_PROMPT, { behavior: 'allow', outcome: 'proceed_once' })))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(BASH, GENERIC_PERMISSION_PROMPT, { behavior: 'allow', permissionScope: 'session' })))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(BASH, GENERIC_PERMISSION_PROMPT, { behavior: 'deny' })))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads a refusal with the reader\'s words as the feedback it sent', () => {
    expect(display(saved(BASH, GENERIC_PERMISSION_PROMPT, { behavior: 'deny', message: 'Use a dry run.', reason: 'Use a dry run.' })))
      .toStrictEqual({ kind: 'feedback', message: 'Use a dry run.' })
    expect(display(saved(BASH, GENERIC_PERMISSION_PROMPT, { behavior: 'deny', message: CONTROL_ANSWER_MESSAGE.RejectedByUser })))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads a plan answer with the words of the plan control', () => {
    expect(display(saved(EXIT_PLAN, PLAN_PROMPT, {
      behavior: 'allow',
      updatedInput: { plan: 'Do the thing.' },
    })))
      .toStrictEqual({ kind: 'label', text: 'Approve' })
    expect(display(saved(EXIT_PLAN, PLAN_PROMPT, { behavior: 'deny' })))
      .toStrictEqual({ kind: 'label', text: 'Reject' })
    expect(display(saved(EXIT_PLAN, PLAN_PROMPT, { behavior: 'deny', message: 'Revise step 2.', reason: 'Revise step 2.' })))
      .toStrictEqual({ kind: 'feedback', message: 'Revise step 2.' })
  })

  // The recorded prompt is what the banner drew, so it decides the words, and the
  // native request does not.
  it('takes the words from the recorded prompt, not from the native request', () => {
    expect(display(saved(EXIT_PLAN, GENERIC_PERMISSION_PROMPT, { behavior: 'allow' })))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(BASH, PLAN_PROMPT, { behavior: 'deny' })))
      .toStrictEqual({ kind: 'label', text: 'Reject' })
  })

  // A row that states no prompt cannot state a plan, so it takes the permission words.
  it('reads an answer whose row states no prompt with the permission words', () => {
    const { prompt: _prompt, ...unprompted } = saved(EXIT_PLAN, PLAN_PROMPT, { behavior: 'allow' })
    expect(display(unprompted)).toStrictEqual({ kind: 'label', text: 'Allow' })
  })

  // The shared display reads a saved MCP elicitation answer before this one, through the
  // recorded elicitation prompt. Qoder's display recognizes no elicitation result itself.
  it('reads nothing from an elicitation result', () => {
    expect(qoderControls.controlResponseDisplay?.({
      requestId: 'form-1',
      claimToken: '',
      request: {
        type: 'control_request',
        request_id: 'form-1',
        request: { subtype: 'elicitation' },
      },
      response: storedAnswer({
        action: 'accept',
        content: { count: 0 },
      }),
    })).toBeNull()
  })

  it('degrades to the generic label for an answer that states no behavior', () => {
    expect(display(saved(BASH, GENERIC_PERMISSION_PROMPT, { outcome: 'proceed_once' })))
      .toStrictEqual({ kind: 'label', text: 'Responded' })
    expect(display({
      ...saved(BASH, GENERIC_PERMISSION_PROMPT, {}),
      response: undefined,
    }))
      .toStrictEqual({ kind: 'label', text: 'Responded' })
  })
})

// Auto approves safe calls and asks about the rest. Bypass is Qoder's own
// bypassPermissions mode. Don't Ask is neither: it denies the calls that Qoder
// does not approve in advance.
describe('qoderControls permissionPresets', () => {
  it('maps Smart to Auto and Bypass to the native bypass mode', () => {
    expect(qoderControls.permissionPresets).toEqual({
      smart: {
        sets: { permissionMode: QODER_MODE.Auto },
      },
      bypass: {
        sets: { permissionMode: QODER_MODE.BypassPermissions },
      },
    })
  })
})

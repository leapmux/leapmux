import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { CODEBUDDY_CONTROL_ANSWER_FIELD } from '~/generated/contracts/codebuddy-protocol'
import { CONTROL_ANSWER_MESSAGE } from '~/generated/contracts/control-answer'
import { GENERIC_PERMISSION_PROMPT } from '~/test-support/controlRequests'
import { } from '~/test-support/controlResponses'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { codebuddyControls } from './pluginControls'

/** The tools whose requests these cases answer. */
type Tool = 'Bash' | 'ExitPlanMode'

/** A stored can_use_tool request in the shape the worker publishes it. */
function request(toolName: Tool): Record<string, unknown> {
  return {
    type: 'control_request',
    request_id: 'perm_1',
    request: {
      subtype: 'can_use_tool',
      tool_name: toolName,
      tool_use_id: 'call-1',
      input: {},
    },
  }
}

/** The prompt that the Worker records for the plan tool: the plan approval. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }

/** The prompt that the Worker records with the answer to each tool's request. */
const PROMPTS: Record<Tool, ControlPrompt> = {
  Bash: GENERIC_PERMISSION_PROMPT,
  ExitPlanMode: PLAN_PROMPT,
}

/**
 * The answer that the worker translated and stored: CodeBuddy's own `{allowed, reason}` object,
 * with the native request and the prompt that the Worker read from it.
 */
function saved(toolName: Tool, answer: Record<string, unknown>): PersistedControlResponse {
  return {
    requestId: 'perm_1',
    claimToken: 'claim',
    request: request(toolName),
    response: {
      type: 'control_response',
      request_id: 'perm_1',
      response: { subtype: 'success', request_id: 'perm_1', response: answer },
    },
    prompt: PROMPTS[toolName],
  }
}

/** The saved row of one stored answer, through the plugin's own display and the shared fallback. */
function display(cr: PersistedControlResponse) {
  return resolveControlResponseSummary(cr, codebuddyControls.controlResponseDisplay)
}

describe('codebuddyControls controlResponseDisplay', () => {
  it('reads an allowed permission as the word its button carried', () => {
    expect(display(saved('Bash', {
      [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: true,
      updatedInput: { command: 'ls' },
    })))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
  })

  it('reads a bare denial as the word its button carried', () => {
    expect(display(saved('Bash', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: false, [CODEBUDDY_CONTROL_ANSWER_FIELD.Reason]: CONTROL_ANSWER_MESSAGE.RejectedByUser })))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads a denial with the reader\'s reason as the feedback it sent', () => {
    expect(display(saved('Bash', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: false, [CODEBUDDY_CONTROL_ANSWER_FIELD.Reason]: '  Use a dry run.  ' })))
      .toStrictEqual({ kind: 'feedback', message: 'Use a dry run.' })
  })

  it('reads a denial with no reason field as the bare denial', () => {
    expect(display(saved('Bash', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: false })))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads a plan answer with the words of the plan control', () => {
    expect(display(saved('ExitPlanMode', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: true })))
      .toStrictEqual({ kind: 'label', text: 'Approve' })
    expect(display(saved('ExitPlanMode', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: false, [CODEBUDDY_CONTROL_ANSWER_FIELD.Reason]: CONTROL_ANSWER_MESSAGE.RejectedByUser })))
      .toStrictEqual({ kind: 'label', text: 'Reject' })
  })

  // The recorded prompt is what the banner drew, so it decides the words, and the
  // native request does not.
  it('takes the words from the recorded prompt, not from the native request', () => {
    expect(display({
      ...saved('ExitPlanMode', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: true }),
      prompt: GENERIC_PERMISSION_PROMPT,
    }))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(display({
      ...saved('Bash', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: false }),
      prompt: PLAN_PROMPT,
    }))
      .toStrictEqual({ kind: 'label', text: 'Reject' })
  })

  // A row that states no prompt still states the decision. The permission words
  // serve, because only a recorded plan prompt can state the plan words.
  it('reads an answer whose row states no prompt with the permission words', () => {
    const { prompt: _prompt, ...unprompted } = saved('ExitPlanMode', { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: true })
    expect(display(unprompted))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
  })

  // A non-boolean flag states no decision. The row then degrades to the shared
  // fallback rather than to a decision nobody made.
  it.each([
    [
      'a string flag',
      { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: 'true' },
    ],
    [
      'no flag',
      { [CODEBUDDY_CONTROL_ANSWER_FIELD.Reason]: 'Use a dry run.' },
    ],
    [
      'a null flag',
      { [CODEBUDDY_CONTROL_ANSWER_FIELD.Allowed]: null },
    ],
  ])('degrades to the generic label for %s', (_name, answer) => {
    expect(display(saved('Bash', answer))).toStrictEqual({ kind: 'label', text: 'Responded' })
  })

  it('degrades to the generic label for a response that is not a CodeBuddy answer', () => {
    expect(display({
      ...saved('Bash', {}),
      response: undefined,
    })).toStrictEqual({ kind: 'label', text: 'Responded' })
    expect(display({
      ...saved('Bash', {}),
      response: { type: 'control_response', response: 'allowed' },
    })).toStrictEqual({ kind: 'label', text: 'Responded' })
  })
})

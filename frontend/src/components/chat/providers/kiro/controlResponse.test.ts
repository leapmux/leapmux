import type { ControlPrompt, PermissionOption } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { kiroControlResponseSummary } from './controlResponse'

/** One saved row: the request, Kiro's reply, and the prompt that the Worker recorded beside them. */
function record(request: Record<string, unknown> | undefined, result: Record<string, unknown> | undefined, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'jsonrpc:1',
    claimToken: 'claim',
    request,
    response: result === undefined
      ? undefined
      : { jsonrpc: '2.0', id: 1, result },
    ...(prompt
      ? { prompt }
      : {}),
  }
}

const QUESTION = {
  jsonrpc: '2.0',
  id: 1,
  method: '_kiro/userInput',
  params: {
    question: 'Which DB?',
    options: [
      { title: 'Postgres' },
    ],
  },
}

/** The prompt that the Worker reads from {@link QUESTION} (`kiroUserInputQuestions` in Go). */
const QUESTION_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      question: 'Which DB?',
      options: [
        { label: 'Postgres', value: 'Postgres' },
      ],
    },
  ],
}

const ELICIT = {
  jsonrpc: '2.0',
  id: 1,
  method: '_kiro/mcp/elicitation',
  params: {
    elicitation: {
      mode: 'form',
      message: 'Choose',
      requestedSchema: {
        type: 'object',
        properties: {
          size: { type: 'string', title: 'Size' },
        },
      },
    },
  },
}

/** The prompt that the Worker reads from {@link ELICIT} (`kiroElicitation` in Go). */
const ELICIT_PROMPT: ControlPrompt = {
  kind: 'elicitation',
  elicitation: {
    mode: 'form',
    message: 'Choose',
    schema: ELICIT.params.elicitation.requestedSchema,
  },
}

const PERMISSION = {
  jsonrpc: '2.0',
  id: 1,
  method: 'session/request_permission',
  params: {
    toolCall: { toolCallId: 'c' },
    options: [
      { optionId: 'accept', name: 'Allow', kind: 'allow_once' },
      { optionId: 'always-accept', name: 'Always allow', kind: 'allow_always' },
      { optionId: 'reject', name: 'Deny', kind: 'reject_once' },
      { optionId: 'always-reject', name: 'Always deny', kind: 'reject_always' },
    ],
    _meta: {
      kiro: {
        consent: { workspaceRoot: '/w' },
      },
    },
  },
}

/**
 * The options that the Worker states for {@link PERMISSION}: Kiro's own always options
 * renamed for the session, and one option of LeapMux's own for each wider scope
 * (`kiroPermissionOptions` in Go).
 */
const PERMISSION_OPTIONS: PermissionOption[] = [
  { optionId: 'accept', kind: 'allow_once', name: 'Allow' },
  {
    optionId: 'always-accept',
    kind: 'allow_always',
    name: 'Always allow for this session',
    scope: 'session',
  },
  {
    optionId: 'always-accept-workspace',
    kind: 'allow_always',
    name: 'Always allow in this workspace',
    scope: 'workspace',
  },
  {
    optionId: 'always-accept-user',
    kind: 'allow_always',
    name: 'Always allow everywhere',
    scope: 'user',
  },
  { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
  {
    optionId: 'always-reject',
    kind: 'reject_always',
    name: 'Always deny for this session',
    scope: 'session',
  },
  {
    optionId: 'always-reject-workspace',
    kind: 'reject_always',
    name: 'Always deny in this workspace',
    scope: 'workspace',
  },
  {
    optionId: 'always-reject-user',
    kind: 'reject_always',
    name: 'Always deny everywhere',
    scope: 'user',
  },
]

const PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: { title: 'echo hi', options: PERMISSION_OPTIONS },
}

/** One saved permission reply, read against the recorded prompt. */
function permission(result: Record<string, unknown>, prompt: ControlPrompt = PERMISSION_PROMPT) {
  return kiroControlResponseSummary(record(PERMISSION, result, prompt))
}

/** A reply that selects one option, with an optional `_meta.kiro`. */
function selected(optionId: string, kiro?: Record<string, unknown>): Record<string, unknown> {
  return {
    outcome: { outcome: 'selected', optionId },
    ...(kiro
      ? {
          _meta: { kiro },
        }
      : {}),
  }
}

describe('kiroControlResponseSummary', () => {
  it('shows the answer of a question, and a dismissal', () => {
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'answered', answer: 'Postgres [PostGIS]' }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Postgres [PostGIS]' })
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'dismissed' }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Dismissed' })
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'other' }, QUESTION_PROMPT))).toBeNull()
  })

  it('leaves an MCP form answer to the shared form display', () => {
    // The row extraction asks the shared form display before this one, so this one
    // reads nothing for a form.
    expect(kiroControlResponseSummary(record(ELICIT, {
      action: 'accept',
      content: { size: 'Large' },
    }, ELICIT_PROMPT))).toBeNull()
  })

  it('shows the reason a rejected permission carried', () => {
    expect(permission(selected('reject', { rejectionReason: ' Use rg ' }))).toEqual({ kind: 'feedback', message: 'Use rg' })
  })

  it('shows the scope of an always-allow in both forms a saved reply can hold', () => {
    expect(permission(selected('always-accept', {
      consent: { scope: 'workspace' },
    }))).toEqual({ kind: 'label', text: 'Always allow in this workspace' })
    expect(permission(selected('always-accept-user'))).toEqual({ kind: 'label', text: 'Always allow everywhere' })
    expect(permission(selected('always-accept'))).toEqual({ kind: 'label', text: 'Always allow for this session' })
    expect(permission(selected('always-accept', {
      consent: { scope: 'toString' },
    }))).toEqual({ kind: 'label', text: 'Always allow for this session' })
  })

  it('shows the scope of an always-deny in both forms a saved reply can hold', () => {
    expect(permission(selected('always-reject', {
      consent: { scope: 'workspace' },
    }))).toEqual({ kind: 'label', text: 'Always deny in this workspace' })
    expect(permission(selected('always-reject-user'))).toEqual({ kind: 'label', text: 'Always deny everywhere' })
    expect(permission(selected('always-reject'))).toEqual({ kind: 'label', text: 'Always deny for this session' })
  })

  it('shows the chosen option of a permission', () => {
    expect(permission(selected('accept'))).toEqual({ kind: 'label', text: 'Allow' })
    expect(permission(selected('reject'))).toEqual({ kind: 'label', text: 'Deny' })
  })

  // The words come from the prompt that the banner drew, so a prompt whose options
  // state other words reads those, whatever the native request holds.
  it('reads the words of the recorded prompt, not of the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'permission',
      permission: {
        options: [
          { optionId: 'accept', kind: 'allow_once', name: 'Run it' },
          {
            optionId: 'always-accept',
            kind: 'allow_always',
            name: 'Keep running it here',
            scope: 'session',
          },
          {
            optionId: 'always-accept-user',
            kind: 'allow_always',
            name: 'Keep running it anywhere',
            scope: 'user',
          },
        ],
      },
    }
    expect(permission(selected('accept'), prompt)).toEqual({ kind: 'label', text: 'Run it' })
    expect(permission(selected('always-accept'), prompt)).toEqual({ kind: 'label', text: 'Keep running it here' })
    expect(permission(selected('always-accept', {
      consent: { scope: 'user' },
    }), prompt)).toEqual({ kind: 'label', text: 'Keep running it anywhere' })
  })

  it('reads the kind of an option that states no name of its own', () => {
    const prompt: ControlPrompt = {
      kind: 'permission',
      permission: {
        options: [
          { optionId: 'accept', kind: 'allow_once' },
        ],
      },
    }
    expect(permission(selected('accept'), prompt)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // The Worker refuses a scope that the prompt does not offer, such as the workspace
  // of a request that states no workspace root.
  it('reads nothing for an option that the prompt does not offer', () => {
    const prompt: ControlPrompt = {
      kind: 'permission',
      permission: {
        options: PERMISSION_OPTIONS.filter(option => option.scope !== 'workspace'),
      },
    }
    expect(permission(selected('always-accept', {
      consent: { scope: 'workspace' },
    }), prompt)).toBeNull()
    expect(permission(selected('a-later-option'))).toBeNull()
    expect(permission(selected('toString'))).toBeNull()
  })

  // A reply that selects no option is the protocol's cancellation, which the shared
  // permission display reads.
  it('shows a cancelled permission', () => {
    expect(permission({
      outcome: { outcome: 'cancelled' },
    })).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(permission({ outcome: {} })).toBeNull()
  })

  // A reason of only whitespace is no reason, so the row states the button instead.
  it('shows the rule of an always option when the rejection reason is only whitespace', () => {
    expect(permission(selected('always-reject', {
      rejectionReason: '  ',
      consent: { scope: 'user' },
    }))).toEqual({ kind: 'label', text: 'Always deny everywhere' })
  })

  it('shows the user scope of Kiro\'s own always-allow', () => {
    expect(permission(selected('always-accept', {
      consent: { scope: 'user' },
    }))).toEqual({ kind: 'label', text: 'Always allow everywhere' })
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads its words.
  it('shows the chosen option of a permission whose native request is gone', () => {
    expect(kiroControlResponseSummary(record(undefined, selected('always-accept', {
      consent: { scope: 'workspace' },
    }), PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Always allow in this workspace' })
  })

  it('shows no answer of a question that saved no answer', () => {
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'answered', answer: '  ' }, QUESTION_PROMPT))).toBeNull()
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'answered' }, QUESTION_PROMPT))).toBeNull()
  })

  it('falls back to the shared display when the reply is no result', () => {
    expect(kiroControlResponseSummary(record(QUESTION, undefined, QUESTION_PROMPT))).toBeNull()
  })

  // The prompt states which request the answer belongs to. A row with none reads as
  // nothing here, and the shared chokepoint shows its generic label.
  it('reads nothing from a row that records no prompt', () => {
    const cr = record(PERMISSION, selected('accept'))
    expect(kiroControlResponseSummary(cr)).toBeNull()
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'dismissed' }))).toBeNull()
    expect(resolveControlResponseSummary(cr, kiroControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })
})

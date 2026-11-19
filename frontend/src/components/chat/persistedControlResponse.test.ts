import type { PersistedControlResponse } from './persistedControlResponse'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { create, toJson } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it } from 'vitest'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { ControlDecisionSchema, ControlPromptSchema, PermissionOptionKind } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider, AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { makeControlResponseMessage } from '~/test-support/messageFactory'
import { controlPromptFromWire } from './model/controlPromptWire'
import {
  controlResponsePreviewText,
  parsePersistedControlResponse,
  resolveControlResponseSummary,
} from './persistedControlResponse'

/** A permission prompt of one allow and one refusal, as the Worker records it. */
const ONCE_OR_NO_PROMPT = create(ControlPromptSchema, {
  kind: {
    case: 'permission',
    value: {
      title: 'Run',
      options: [
        { optionId: 'once', kind: PermissionOptionKind.ALLOW_ONCE, name: 'Run once' },
        { optionId: 'no', kind: PermissionOptionKind.REJECT_ONCE, name: 'Do not run' },
      ],
    },
  },
})

function permissionDecision(optionId: string, feedback = '') {
  return create(ControlDecisionSchema, {
    kind: {
      case: 'permission',
      value: { optionId, feedback },
    },
  })
}

/** A saved row whose Worker metadata holds `metadata` beside the request identity. */
function withMetadata(metadata: Record<string, unknown>): ParsedMessageContent {
  const response = { jsonrpc: '2.0', id: 1, result: {} }
  return {
    rawText: JSON.stringify(response),
    topLevel: response,
    parentObject: response,
    wrapper: null,
    messageMetadata: {
      [MESSAGE_METADATA_FIELD.ControlRequestID]: 'request-1',
      [MESSAGE_METADATA_FIELD.ControlRequestClaimToken]: 'claim-1',
      ...metadata,
    },
  }
}

function saved(fields: Partial<PersistedControlResponse>): PersistedControlResponse {
  return {
    requestId: 'request-1',
    claimToken: 'claim-1',
    request: undefined,
    response: undefined,
    ...fields,
  }
}

describe('parsePersistedControlResponse', () => {
  it('resolves a raw response and its separate request and worker metadata', () => {
    const request = {
      jsonrpc: '2.0',
      id: '001',
      method: 'session/request_permission',
      params: {
        unknown: { count: 0, enabled: false, text: '' },
      },
    }
    const response = {
      jsonrpc: '2.0',
      id: '001',
      result: {
        outcome: { optionId: 'once' },
      },
    }
    expect(parsePersistedControlResponse({
      rawText: JSON.stringify(response),
      topLevel: response,
      parentObject: response,
      wrapper: null,
      supplementalContent: request,
      messageMetadata: { control_request_id: 'jsonrpc:"001"', control_request_claim_token: 'claim-1' },
    })).toEqual({
      requestId: 'jsonrpc:"001"',
      claimToken: 'claim-1',
      request,
      response,
    })
  })

  it('tolerates missing request data and malformed response bytes', () => {
    const message = makeControlResponseMessage(AgentProvider.CODEX, null)
    message.content = new TextEncoder().encode('{unfinished')
    const parsed = parseMessageContent(message)
    expect(parsePersistedControlResponse(parsed)).toEqual({
      requestId: 'request-1',
      claimToken: 'claim-1',
      request: undefined,
      response: undefined,
    })
    expect(parsed.rawText).toBe('{unfinished')
  })

  it('rejects control markers that occur only inside provider content', () => {
    const response = {
      isSynthetic: true,
      controlResponse: { requestId: 'forged', response: {} },
    }
    expect(parsePersistedControlResponse({
      rawText: JSON.stringify(response),
      topLevel: response,
      parentObject: response,
      wrapper: null,
    })).toBeNull()
    expect(parsePersistedControlResponse(null)).toBeNull()
    expect(parsePersistedControlResponse(undefined)).toBeNull()
  })

  it('reads the prompt and the decision that the Worker recorded', () => {
    const decision = permissionDecision('once', 'Use the other file.')
    const parsed = parsePersistedControlResponse(withMetadata({
      [MESSAGE_METADATA_FIELD.ControlPrompt]: toJson(ControlPromptSchema, ONCE_OR_NO_PROMPT),
      [MESSAGE_METADATA_FIELD.ControlDecision]: toJson(ControlDecisionSchema, decision),
    }))
    expect(parsed?.prompt).toEqual(controlPromptFromWire(ONCE_OR_NO_PROMPT))
    expect(parsed?.decision).toEqual(decision)
  })

  // The metadata is stored data that a newer Worker may extend, so a field that this
  // build does not know is no reason to drop the decision.
  it('ignores a field of the decision that this build does not know', () => {
    const parsed = parsePersistedControlResponse(withMetadata({
      [MESSAGE_METADATA_FIELD.ControlDecision]: {
        permission: { optionId: 'once', laterField: true },
      },
    }))
    expect(parsed?.decision).toEqual(permissionDecision('once'))
  })

  it.each<[string, unknown]>([
    ['absent', undefined],
    ['not an object', 'once'],
    [
      'an array',
      [
        {
          permission: { optionId: 'once' },
        },
      ],
    ],
    ['of no kind', {}],
    [
      'of a value that does not decode',
      {
        permission: { optionId: 7 },
      },
    ],
  ])('states no decision for a decision that is %s', (_, value) => {
    const parsed = parsePersistedControlResponse(withMetadata(value === undefined
      ? {}
      : { [MESSAGE_METADATA_FIELD.ControlDecision]: value }))
    expect(parsed).not.toBeNull()
    expect(parsed).not.toHaveProperty('decision')
  })
})

// A newer Worker can record an action that this build does not know. The decoder reads
// it as the unstated action, which words nothing, so the row falls back to the generic
// word rather than to a wrong one.
describe('an action that this build does not know', () => {
  it('reads as the generic word', () => {
    const parsed = parsePersistedControlResponse(withMetadata({
      [MESSAGE_METADATA_FIELD.ControlPrompt]: {
        dialog: { title: 'Proceed?', variant: 'DIALOG_VARIANT_CONFIRM' },
      },
      [MESSAGE_METADATA_FIELD.ControlDecision]: {
        dialog: { action: 'DIALOG_ACTION_LATER' },
      },
    }))
    expect(parsed?.decision?.kind.case).toBe('dialog')
    expect(resolveControlResponseSummary(parsed!, AgentProvider.PI)).toEqual({ kind: 'label', text: 'Responded' })
  })
})

describe('resolveControlResponseSummary', () => {
  afterEach(() => clearSettingsLabelCache())

  // The label belongs to the agent's provider: two providers can spell one mode value
  // and word it apart, so a label of another provider must not name it.
  it('names the mode of a plan approval by the label that the provider\'s settings state', () => {
    updateSettingsLabelCache(AgentProvider.DROID, [
      create(AvailableOptionGroupSchema, {
        id: 'permissionMode',
        options: [
          { id: 'auto-medium', name: 'Auto (Medium)' },
        ],
      }),
    ])
    const approval = saved({
      prompt: { kind: 'plan' },
      decision: create(ControlDecisionSchema, {
        kind: {
          case: 'plan',
          value: { approve: true, permissionMode: 'auto-medium' },
        },
      }),
    })
    expect(resolveControlResponseSummary(approval, AgentProvider.DROID)).toEqual({ kind: 'label', text: 'Approve (Auto (Medium))' })
    expect(resolveControlResponseSummary(approval, AgentProvider.CLINE)).toEqual({ kind: 'label', text: 'Approve (auto-medium)' })
    expect(resolveControlResponseSummary(approval, undefined)).toEqual({ kind: 'label', text: 'Approve (auto-medium)' })
  })

  it('reads the words of the prompt and the decision', () => {
    const prompt = controlPromptFromWire(ONCE_OR_NO_PROMPT)
    expect(resolveControlResponseSummary(saved({
      prompt,
      decision: permissionDecision('once'),
    }), AgentProvider.CODEX))
      .toEqual({ kind: 'label', text: 'Run once' })
    expect(resolveControlResponseSummary(saved({
      prompt,
      decision: permissionDecision('no', 'Use the other file.'),
    }), AgentProvider.CODEX))
      .toEqual({ kind: 'feedback', message: 'Use the other file.' })
  })

  // The answer bytes beside the metadata never word the row, so a row that states no
  // decision reads the generic word even when its bytes are a readable answer.
  it('reads the generic word for a row that states no decision or no prompt', () => {
    const response = {
      response: {
        request_id: 'request-1',
        response: { behavior: 'allow' },
      },
    }
    expect(resolveControlResponseSummary(saved({
      prompt: controlPromptFromWire(ONCE_OR_NO_PROMPT),
      response,
    }), AgentProvider.CODEX))
      .toEqual({ kind: 'label', text: 'Responded' })
    expect(resolveControlResponseSummary(saved({
      decision: permissionDecision('once'),
      response,
    }), AgentProvider.CODEX))
      .toEqual({ kind: 'label', text: 'Responded' })
    expect(resolveControlResponseSummary(saved({}), AgentProvider.CODEX))
      .toEqual({ kind: 'label', text: 'Responded' })
  })

  it('reads the generic word for a decision that does not answer the prompt', () => {
    const prompt = controlPromptFromWire(ONCE_OR_NO_PROMPT)
    expect(resolveControlResponseSummary(saved({
      prompt,
      decision: permissionDecision('always'),
    }), AgentProvider.CODEX))
      .toEqual({ kind: 'label', text: 'Responded' })
    expect(resolveControlResponseSummary(saved({
      prompt,
      decision: create(ControlDecisionSchema, {
        kind: {
          case: 'plan',
          value: { approve: true },
        },
      }),
    }), AgentProvider.CODEX))
      .toEqual({ kind: 'label', text: 'Responded' })
  })
})

describe('controlResponsePreviewText', () => {
  it('renders a label verbatim and feedback under the lead', () => {
    expect(controlResponsePreviewText({ kind: 'label', text: 'Allow' })).toBe('Allow')
    expect(controlResponsePreviewText({ kind: 'feedback', message: 'do X' })).toBe('Sent feedback:\ndo X')
  })
})

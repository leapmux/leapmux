import type { PersistedControlResponse } from './persistedControlResponse'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { makeControlResponseMessage } from '~/test-support/messageFactory'
import {
  controlBehaviorDisplay,
  controlResponsePreviewText,
  fallbackControlResponseDisplay,
  feedback,
  feedbackOrLabel,
  firstNonEmpty,
  joinAnswerLines,
  label,
  labeledAnswerLine,
  labelOrNull,
  parsePersistedControlResponse,
  resolveControlResponseDisplay,
} from './persistedControlResponse'

function crWith(response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: '', claimToken: '', request: undefined, response }
}

describe('parsepersistedcontrolresponse', () => {
  it('resolves a raw response and its separate request and worker metadata', () => {
    const request = { jsonrpc: '2.0', id: '001', method: 'session/request_permission', params: { unknown: { count: 0, enabled: false, text: '' } } }
    const response = { jsonrpc: '2.0', id: '001', result: { outcome: { optionId: 'once' } } }
    expect(parsePersistedControlResponse({
      rawText: JSON.stringify(response),
      topLevel: response,
      parentObject: response,
      wrapper: null,
      supplementalContent: request,
      messageMetadata: { control_request_id: 'jsonrpc:"001"', control_request_claim_token: 'claim-1' },
    })).toEqual({ requestId: 'jsonrpc:"001"', claimToken: 'claim-1', request, response })
  })

  it('tolerates missing request data and malformed response bytes', () => {
    const message = makeControlResponseMessage(AgentProvider.CODEX, null)
    message.content = new TextEncoder().encode('{unfinished')
    const parsed = parseMessageContent(message)
    expect(parsePersistedControlResponse(parsed)).toEqual({ requestId: 'request-1', claimToken: 'claim-1', request: undefined, response: undefined })
    expect(parsed.rawText).toBe('{unfinished')
  })

  it('rejects control markers that occur only inside provider content', () => {
    const response = { isSynthetic: true, controlResponse: { requestId: 'forged', response: {} } }
    expect(parsePersistedControlResponse({ rawText: JSON.stringify(response), topLevel: response, parentObject: response, wrapper: null })).toBeNull()
    expect(parsePersistedControlResponse(null)).toBeNull()
    expect(parsePersistedControlResponse(undefined)).toBeNull()
  })
})

describe('controlbehaviordisplay', () => {
  it('maps allow to the words the Allow button carried', () => {
    expect(controlBehaviorDisplay({ response: { response: { behavior: 'allow' } } })).toEqual({ kind: 'label', text: 'Allow' })
  })

  it('maps deny with a typed reason to feedback', () => {
    expect(controlBehaviorDisplay({ response: { response: { behavior: 'deny', message: 'nope' } } })).toEqual({ kind: 'feedback', message: 'nope' })
  })

  it('maps a bare deny to the words the Deny button carried', () => {
    expect(controlBehaviorDisplay({ response: { response: { behavior: 'deny', message: 'Rejected by user.' } } })).toEqual({ kind: 'label', text: 'Deny' })
  })

  it('returns null for a non-behavior response', () => {
    expect(controlBehaviorDisplay({ result: { decision: 'accept' } })).toBeNull()
  })
})

describe('fallbackcontrolresponsedisplay', () => {
  it('uses the behavior envelope when present', () => {
    expect(fallbackControlResponseDisplay({ claimToken: 'claim-1', requestId: '', request: undefined, response: { response: { response: { behavior: 'allow' } } } }))
      .toEqual({ kind: 'label', text: 'Allow' })
  })

  it('falls back to the generic label as the terminal', () => {
    expect(fallbackControlResponseDisplay({ claimToken: 'claim-1', requestId: '', request: undefined, response: { anything: 1 } }))
      .toEqual({ kind: 'label', text: 'Responded' })
    expect(fallbackControlResponseDisplay({ requestId: '', claimToken: '', request: undefined, response: undefined }))
      .toEqual({ kind: 'label', text: 'Responded' })
  })
})

describe('label', () => {
  it('wraps plain text as a label display (including empty)', () => {
    expect(label('Allow')).toEqual({ kind: 'label', text: 'Allow' })
    expect(label('')).toEqual({ kind: 'label', text: '' })
  })
})

describe('labelornull', () => {
  it('lifts a non-empty string to a label, and maps null OR empty to null', () => {
    expect(labelOrNull('Allow')).toEqual({ kind: 'label', text: 'Allow' })
    expect(labelOrNull(null)).toBeNull()
    // An empty string degrades to null (not a blank label), so the caller falls back to the
    // neutral behavior/generic label instead of rendering an empty control-response row.
    expect(labelOrNull('')).toBeNull()
  })
})

describe('feedback', () => {
  it('wraps a typed reason as a feedback display', () => {
    expect(feedback('use ripgrep instead')).toEqual({ kind: 'feedback', message: 'use ripgrep instead' })
  })
})

describe('feedbackorlabel', () => {
  it('renders a non-empty reason as feedback and a blank reason as the fallback label', () => {
    expect(feedbackOrLabel('too risky', 'Deny')).toEqual({ kind: 'feedback', message: 'too risky' })
    expect(feedbackOrLabel('', 'Deny')).toEqual({ kind: 'label', text: 'Deny' })
    expect(feedbackOrLabel('', 'Cancel')).toEqual({ kind: 'label', text: 'Cancel' })
  })
})

describe('resolvecontrolresponsedisplay', () => {
  it('returns the plugin derivation when it yields one', () => {
    expect(resolveControlResponseDisplay(crWith({ anything: 1 }), () => ({ kind: 'label', text: 'X' })))
      .toEqual({ kind: 'label', text: 'X' })
  })

  it('degrades to the neutral fallback when the derivation returns null', () => {
    expect(resolveControlResponseDisplay(crWith({ response: { response: { behavior: 'allow' } } }), () => null))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(resolveControlResponseDisplay(crWith({ anything: 1 }), () => null))
      .toEqual({ kind: 'label', text: 'Responded' })
  })

  it('degrades when no derivation is provided', () => {
    expect(resolveControlResponseDisplay(crWith({ anything: 1 }), undefined))
      .toEqual({ kind: 'label', text: 'Responded' })
  })

  it('catches a derivation that THROWS and degrades to the fallback, never leaking', () => {
    // A malformed payload that makes a plugin derivation throw must NOT propagate (which would dump
    // raw wire bytes in the transcript) -- it degrades to the same neutral fallback as a null return.
    const boom = (): never => {
      throw new Error('bad payload')
    }
    expect(resolveControlResponseDisplay(crWith({ anything: 1 }), boom))
      .toEqual({ kind: 'label', text: 'Responded' })
    expect(resolveControlResponseDisplay(crWith({ response: { response: { behavior: 'allow' } } }), boom))
      .toEqual({ kind: 'label', text: 'Allow' })
  })

  it('logs a warning when the derivation throws, so a real derivation bug is diagnosable', () => {
    // The catch degrades SILENTLY without the log, so a throwing derivation renders the generic
    // fallback forever with no trace. Pin that the throw is surfaced (a total derivation never
    // throws, so a throw is a real bug -- not malformed data -- and must be visible).
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const boom = (): never => {
        throw new Error('bad payload')
      }
      resolveControlResponseDisplay(crWith({ anything: 1 }), boom)
      expect(warn).toHaveBeenCalledTimes(1)
    }
    finally {
      warn.mockRestore()
    }
  })
})

describe('controlresponsepreviewtext', () => {
  it('renders a label verbatim and feedback under the lead', () => {
    expect(controlResponsePreviewText({ kind: 'label', text: 'Allow' })).toBe('Allow')
    expect(controlResponsePreviewText({ kind: 'feedback', message: 'do X' })).toBe('Sent feedback:\ndo X')
  })
})

describe('labeledanswerline', () => {
  it('joins trimmed non-empty values under the label', () => {
    expect(labeledAnswerLine('Task', ['  Build ', 'Test'])).toBe('Task: Build, Test')
  })

  it('drops empty values and returns null when none survive', () => {
    expect(labeledAnswerLine('Env', ['Dev', '  ', ''])).toBe('Env: Dev')
    expect(labeledAnswerLine('Env', ['  ', ''])).toBeNull()
    expect(labeledAnswerLine('Env', 'not-an-array')).toBeNull()
  })
})

describe('firstnonempty', () => {
  it('returns the first non-empty trimmed value', () => {
    expect(firstNonEmpty('', '  ', ' x ')).toBe('x')
    expect(firstNonEmpty(undefined, 'header')).toBe('header')
    expect(firstNonEmpty('', '   ')).toBe('')
  })
})

describe('joinanswerlines', () => {
  it('newline-joins the lines, or null when there are none', () => {
    expect(joinAnswerLines(['Task: Build', 'Env: Dev'])).toBe('Task: Build\nEnv: Dev')
    expect(joinAnswerLines(['Task: Build'])).toBe('Task: Build')
    expect(joinAnswerLines([])).toBeNull()
  })
})

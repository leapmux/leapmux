import type { PersistedControlResponse } from './persistedControlResponse'
import { describe, expect, it, vi } from 'vitest'
import {
  controlBehaviorDisplay,
  controlResponsePreviewText,
  fallbackControlResponseDisplay,
  feedback,
  feedbackOrLabel,
  firstNonEmpty,
  isPersistedControlResponse,
  joinAnswerLines,
  label,
  labeledAnswerLine,
  labelOrNull,
  parsePersistedControlResponse,
  resolveControlResponseDisplay,
} from './persistedControlResponse'

function crWith(response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { provider: 'CODEX', requestId: '', request: undefined, response }
}

describe('ispersistedcontrolresponse', () => {
  it('accepts a synthetic row whose controlResponse is an object', () => {
    expect(isPersistedControlResponse({ isSynthetic: true, controlResponse: { provider: 'CODEX' } })).toBe(true)
  })

  it('rejects non-synthetic rows and non-object controlResponse', () => {
    expect(isPersistedControlResponse({ controlResponse: { provider: 'CODEX' } })).toBe(false)
    expect(isPersistedControlResponse({ isSynthetic: true, controlResponse: 'x' })).toBe(false)
    expect(isPersistedControlResponse(null)).toBe(false)
    expect(isPersistedControlResponse(undefined)).toBe(false)
  })
})

describe('parsepersistedcontrolresponse', () => {
  it('parses the full envelope', () => {
    const parsed = parsePersistedControlResponse({
      isSynthetic: true,
      controlResponse: {
        provider: 'OPENCODE',
        requestId: '7',
        request: { method: 'session/request_permission' },
        response: { result: { outcome: { optionId: 'proceed_once' } } },
      },
    })
    expect(parsed).toEqual({
      provider: 'OPENCODE',
      requestId: '7',
      request: { method: 'session/request_permission' },
      response: { result: { outcome: { optionId: 'proceed_once' } } },
    })
  })

  it('defaults missing fields and tolerates an omitted request/response', () => {
    const parsed = parsePersistedControlResponse({ isSynthetic: true, controlResponse: {} })
    expect(parsed).toEqual({ provider: '', requestId: '', request: undefined, response: undefined })
  })

  it('returns null when the shape is not a persisted control response', () => {
    expect(parsePersistedControlResponse({ content: 'hi' })).toBeNull()
    expect(parsePersistedControlResponse(null)).toBeNull()
  })
})

describe('controlbehaviordisplay', () => {
  it('maps allow to Approved', () => {
    expect(controlBehaviorDisplay({ response: { response: { behavior: 'allow' } } })).toEqual({ kind: 'label', text: 'Approved' })
  })

  it('maps deny with a typed reason to feedback', () => {
    expect(controlBehaviorDisplay({ response: { response: { behavior: 'deny', message: 'nope' } } })).toEqual({ kind: 'feedback', message: 'nope' })
  })

  it('maps a bare deny (sentinel-only message) to Rejected', () => {
    expect(controlBehaviorDisplay({ response: { response: { behavior: 'deny', message: 'Rejected by user.' } } })).toEqual({ kind: 'label', text: 'Rejected' })
  })

  it('returns null for a non-behavior response', () => {
    expect(controlBehaviorDisplay({ result: { decision: 'accept' } })).toBeNull()
  })
})

describe('fallbackcontrolresponsedisplay', () => {
  it('uses the behavior envelope when present', () => {
    expect(fallbackControlResponseDisplay({ provider: 'X', requestId: '', request: undefined, response: { response: { response: { behavior: 'allow' } } } }))
      .toEqual({ kind: 'label', text: 'Approved' })
  })

  it('falls back to the generic label as the terminal', () => {
    expect(fallbackControlResponseDisplay({ provider: 'X', requestId: '', request: undefined, response: { anything: 1 } }))
      .toEqual({ kind: 'label', text: 'Responded' })
    expect(fallbackControlResponseDisplay({ provider: 'X', requestId: '', request: undefined, response: undefined }))
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
    expect(feedbackOrLabel('too risky', 'Rejected')).toEqual({ kind: 'feedback', message: 'too risky' })
    expect(feedbackOrLabel('', 'Rejected')).toEqual({ kind: 'label', text: 'Rejected' })
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
      .toEqual({ kind: 'label', text: 'Approved' })
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
      .toEqual({ kind: 'label', text: 'Approved' })
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

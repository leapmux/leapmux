import { describe, expect, it } from 'vitest'
import {
  AMBIENT_SCENARIO_ID,
  collectScenarioIDs,
  contentText,
  lastUserText,
  matchesRequest,
  MAX_STEP_DELAY_MS,
  parseScenarioSpec,
  SCENARIO_MARKER,
  selectScenarioID,
  systemText,
  textChunks,
  validateScenarioID,
} from './mockModelScript'

function request(overrides: Partial<Parameters<typeof matchesRequest>[1]> = {}) {
  return {
    protocol: 'openai-chat-completions' as const,
    systemText: 'You are a coding agent.',
    userText: 'Review the parser.',
    body: { model: 'mock-model' },
    ...overrides,
  }
}

describe('validateScenarioID', () => {
  it('accepts letters, digits, underscores, and hyphens', () => {
    expect(() => validateScenarioID('scenario-1_A')).not.toThrow()
  })

  it('refuses an empty identifier, a long one, and one with a separator', () => {
    for (const id of ['', 'a'.repeat(129), 'a/b', 'a b', 'a.b'])
      expect(() => validateScenarioID(id)).toThrow('1 to 128 ASCII letters')
  })
})

describe('selectScenarioID', () => {
  it('falls back to the ambient scenario when no marker is present', () => {
    expect(selectScenarioID({ messages: [{ role: 'user', content: 'No marker' }] })).toBe(AMBIENT_SCENARIO_ID)
  })

  it('takes the newest marker, so one chat can run several scenarios', () => {
    const body = {
      messages: [
        { role: 'user', content: `First\n\n${SCENARIO_MARKER}older` },
        { role: 'assistant', content: 'Answer' },
        { role: 'user', content: `Second\n\n${SCENARIO_MARKER}newer` },
      ],
    }
    expect(collectScenarioIDs(body)).toEqual(['older', 'newer'])
    expect(selectScenarioID(body)).toBe('newer')
  })

  it('finds a marker nested inside a structured content block', () => {
    const body = { input: [{ role: 'user', content: [{ type: 'input_text', text: `Run\n${SCENARIO_MARKER}deep-1` }] }] }
    expect(selectScenarioID(body)).toBe('deep-1')
  })

  it('ignores a marker whose identifier is empty', () => {
    expect(collectScenarioIDs({ messages: [{ content: `${SCENARIO_MARKER} ` }] })).toEqual([])
  })
})

describe('systemText', () => {
  it('joins a Chat Completions system role', () => {
    expect(systemText({ messages: [{ role: 'system', content: 'Be brief.' }, { role: 'user', content: 'Hi' }] })).toBe('Be brief.')
  })

  it('joins Responses instructions with a developer role', () => {
    expect(systemText({
      instructions: 'Top level.',
      input: [{ role: 'developer', content: [{ type: 'input_text', text: 'Nested.' }] }],
    })).toBe('Top level.\nNested.')
  })

  it('reads the Anthropic top-level system field, whether text or blocks', () => {
    expect(systemText({ system: 'Plain.' })).toBe('Plain.')
    expect(systemText({ system: [{ type: 'text', text: 'Block one.' }, { type: 'text', text: 'Block two.' }] }))
      .toBe('Block one.\nBlock two.')
  })

  it('returns an empty string for a body with no system text', () => {
    expect(systemText({ messages: [{ role: 'user', content: 'Hi' }] })).toBe('')
    expect(systemText(null)).toBe('')
    expect(systemText([])).toBe('')
  })
})

describe('lastUserText', () => {
  it('takes the last user turn, not the first', () => {
    expect(lastUserText({
      messages: [{ role: 'user', content: 'First' }, { role: 'assistant', content: 'Reply' }, { role: 'user', content: 'Second' }],
    })).toBe('Second')
  })

  it('reads a Responses input array', () => {
    expect(lastUserText({ input: [{ role: 'user', content: [{ type: 'input_text', text: 'From input' }] }] })).toBe('From input')
  })

  it('returns an empty string when no user turn exists', () => {
    expect(lastUserText({ messages: [{ role: 'system', content: 'Only system' }] })).toBe('')
  })
})

describe('contentText', () => {
  it('flattens a string, an array, and a text block', () => {
    expect(contentText('plain')).toBe('plain')
    expect(contentText([{ type: 'text', text: 'a' }, { type: 'text', text: 'b' }])).toBe('a\nb')
  })

  it('returns an empty string for a value that carries no text', () => {
    expect(contentText(null)).toBe('')
    expect(contentText(42)).toBe('')
  })
})

describe('matchesRequest', () => {
  it('matches an empty matcher against every request', () => {
    expect(matchesRequest({}, request())).toBe(true)
  })

  it('compares the protocol exactly', () => {
    expect(matchesRequest({ protocol: 'openai-chat-completions' }, request())).toBe(true)
    expect(matchesRequest({ protocol: 'anthropic-messages' }, request())).toBe(false)
  })

  it('matches a pattern without regard to case', () => {
    expect(matchesRequest({ user: 'REVIEW THE PARSER' }, request())).toBe(true)
  })

  it('requires every pattern of an array to match', () => {
    expect(matchesRequest({ user: ['review', 'parser'] }, request())).toBe(true)
    expect(matchesRequest({ user: ['review', 'compiler'] }, request())).toBe(false)
  })

  it('combines the stated fields with AND', () => {
    expect(matchesRequest({ system: 'coding agent', user: 'parser' }, request())).toBe(true)
    expect(matchesRequest({ system: 'coding agent', user: 'compiler' }, request())).toBe(false)
  })

  it('matches the serialized body', () => {
    expect(matchesRequest({ body: '"model":"mock-model"' }, request())).toBe(true)
    expect(matchesRequest({ body: '"model":"other"' }, request())).toBe(false)
  })
})

describe('parseScenarioSpec', () => {
  it('defaults both collections and keeps a step that states only text', () => {
    expect(parseScenarioSpec({ steps: [{ text: 'Answer' }] })).toEqual({ steps: [{ text: 'Answer' }], rules: [] })
    expect(parseScenarioSpec({ rules: [{ name: 'r', respond: { text: 'Answer' } }] }))
      .toEqual({ steps: [], rules: [{ name: 'r', when: {}, respond: { text: 'Answer' } }] })
  })

  it('refuses a script that holds neither a step nor a rule', () => {
    expect(() => parseScenarioSpec({})).toThrow('at least one step, one rule, or a fallback')
    expect(() => parseScenarioSpec({ steps: [], rules: [] })).toThrow('at least one step, one rule, or a fallback')
    expect(() => parseScenarioSpec(null)).toThrow('must be an object')
  })

  it('keeps an empty text, which is a real answer', () => {
    expect(parseScenarioSpec({ steps: [{ text: '' }] }).steps[0]).toEqual({ text: '' })
  })

  it('refuses a step that carries nothing and one that carries both', () => {
    expect(() => parseScenarioSpec({ steps: [{}] })).toThrow('needs text, toolCalls, or error')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', error: { status: 500, message: 'b' } }] }))
      .toThrow('cannot combine an error with output')
  })

  it('validates a tool call and an error', () => {
    expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: '', name: 'Read', arguments: {} }] }] }))
      .toThrow('needs id and name')
    expect(() => parseScenarioSpec({ steps: [{ error: { status: 200, message: 'ok' } }] }))
      .toThrow('needs an HTTP status and message')
  })

  it('takes a tool call with JSON arguments or with raw custom-tool input', () => {
    expect(parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'a', name: 'Read', arguments: { path: 'x' } }] }] }).steps[0])
      .toEqual({ toolCalls: [{ id: 'a', name: 'Read', arguments: { path: 'x' } }] })
    expect(parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'a', name: 'exec', input: 'await tools.exec_command({})' }] }] }).steps[0])
      .toEqual({ toolCalls: [{ id: 'a', name: 'exec', input: 'await tools.exec_command({})' }] })
  })

  it('refuses a tool call that states both forms or neither', () => {
    expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'a', name: 'exec' }] }] }))
      .toThrow('either object arguments or raw text input')
    expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'a', name: 'exec', arguments: {}, input: 'x' }] }] }))
      .toThrow('either object arguments or raw text input')
  })

  it('limits the delay to the interval a run can wait', () => {
    expect(parseScenarioSpec({ steps: [{ text: 'a', delayMs: 0 }] }).steps[0]).toEqual({ text: 'a', delayMs: 0 })
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', delayMs: -1 }] })).toThrow('delayMs must be an integer')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', delayMs: 120_001 }] })).toThrow('delayMs must be an integer')
  })

  it('refuses two rules with one name, because a status counts matches by name', () => {
    expect(() => parseScenarioSpec({
      rules: [
        { name: 'same', respond: { text: 'a' } },
        { name: 'same', respond: { text: 'b' } },
      ],
    })).toThrow('declared twice')
  })

  it('refuses an unknown protocol and an invalid pattern', () => {
    expect(() => parseScenarioSpec({ rules: [{ name: 'r', when: { protocol: 'grpc' }, respond: { text: 'a' } }] }))
      .toThrow('protocol must be one of')
    expect(() => parseScenarioSpec({ rules: [{ name: 'r', when: { user: '[' }, respond: { text: 'a' } }] }))
      .toThrow('not a valid regular expression')
    expect(() => parseScenarioSpec({ rules: [{ name: 'r', when: { user: [] }, respond: { text: 'a' } }] }))
      .toThrow('must state at least one pattern')
  })
})

describe('textChunks', () => {
  it('returns no piece for a step that carries no text', () => {
    expect(textChunks({ toolCalls: [{ id: 't1', name: 'Bash', arguments: { command: 'ls' } }] })).toEqual([])
  })

  it('returns the whole text as one piece when the step does not stream', () => {
    expect(textChunks({ text: 'a complete answer' })).toEqual(['a complete answer'])
  })

  it('splits the text into pieces of the stated size', () => {
    expect(textChunks({ text: 'abcdefg', stream: { chunkChars: 3, delayMs: 5 } }))
      .toEqual(['abc', 'def', 'g'])
  })

  // A text shorter than one piece must not become an empty first piece followed
  // by nothing, which would write a delta carrying no characters.
  it('returns one piece when the text is shorter than the chunk size', () => {
    expect(textChunks({ text: 'ab', stream: { chunkChars: 8, delayMs: 5 } })).toEqual(['ab'])
  })

  it('preserves the text exactly, so the pieces rejoin into the original', () => {
    const text = 'Sorting algorithms compare, partition, and merge.\nEach step costs.'
    const chunks = textChunks({ text, stream: { chunkChars: 7, delayMs: 1 } })
    expect(chunks.join('')).toBe(text)
  })
})

describe('parseScenarioSpec stream', () => {
  it('accepts a stream beside the text it delivers', () => {
    const spec = parseScenarioSpec({ steps: [{ text: 'abc', stream: { chunkChars: 1, delayMs: 10 } }] })
    expect(spec.steps[0]!.stream).toEqual({ chunkChars: 1, delayMs: 10 })
  })

  it('refuses a stream on a step with no text to deliver', () => {
    expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: 't', name: 'Bash', arguments: { command: 'ls' } }], stream: { chunkChars: 1, delayMs: 1 } }] }))
      .toThrow('no text to deliver')
  })

  it('refuses a chunk size below one and a stream with no delay', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', stream: { chunkChars: 0, delayMs: 1 } }] }))
      .toThrow('chunkChars must be an integer of 1 or more')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', stream: { chunkChars: 2 } }] }))
      .toThrow('stream needs delayMs')
  })

  it('holds a stream pause to the same cap as a step delay', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', stream: { chunkChars: 1, delayMs: MAX_STEP_DELAY_MS + 1 } }] }))
      .toThrow('delayMs must be an integer from 0 to')
  })
})

import type { MockModelScenarioStatus } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import {
  AMBIENT_SCENARIO_ID,
  collectScenarioIDs,
  contentText,
  describeScenarioStatus,
  lastUserText,
  matchesRequest,
  MAX_SCENARIO_REQUEST_RECORDS,
  MAX_STEP_DELAY_MS,
  parseScenarioSpec,
  resolveStepCaptures,
  SCENARIO_MARKER,
  selectScenarioID,
  stepRequest,
  systemText,
  textChunks,
  validateScenarioID,
  validateStepIndex,
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

describe('Google request text', () => {
  const body = {
    systemInstruction: { parts: [{ text: 'ACTUAL_SYSTEM' }] },
    contents: [
      { role: 'user', parts: [{ text: 'ACTUAL_USER' }] },
      { role: 'model', parts: [{ text: 'MODEL_ONLY' }] },
      { role: 'user', parts: [{ functionResponse: { response: { text: 'RESULT_ONLY' } } }] },
    ],
  }

  it('reads only native instructions and the last real user text', () => {
    expect(systemText(body)).toBe('ACTUAL_SYSTEM')
    expect(lastUserText(body)).toBe('ACTUAL_USER')
  })

  it('matches the actual final Google user row without reading nested function responses as text', () => {
    const context = { protocol: 'google-generative-language' as const, systemText: 'ACTUAL_SYSTEM', userText: 'ACTUAL_USER', body }
    expect(matchesRequest({ lastMessage: { role: 'user', text: '^$' } }, context)).toBe(true)
    expect(matchesRequest({ lastMessage: { text: 'RESULT_ONLY' } }, context)).toBe(false)
  })
})

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
  it('keeps the actual native prompt marker when later session metadata contains a truncated title', () => {
    const body = {
      model: 'private-native-model',
      messages: [{ role: 'user', content: `Compact this context.\n${SCENARIO_MARKER}native-deepseek-compaction` }],
      dsh_session_log: { events: [{ type: 'session/title', data: { title: `${SCENARIO_MARKER}native-deepseek-compa` } }] },
    }
    expect(selectScenarioID(body)).toBe('native-deepseek-compaction')
  })

  it.each([
    { messages: [{ role: 'user', content: `${SCENARIO_MARKER}current` }, { role: 'assistant', content: `${SCENARIO_MARKER}stale` }] },
    { messages: [{ role: 'user', content: `${SCENARIO_MARKER}current` }, { role: 'tool', content: `${SCENARIO_MARKER}stale` }] },
    { messages: [{ role: 'user', content: `${SCENARIO_MARKER}current` }, { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'old-call', content: `${SCENARIO_MARKER}stale` }] }] },
    { input: [{ role: 'user', content: [{ type: 'input_text', text: `${SCENARIO_MARKER}current` }] }, { type: 'function_call_output', call_id: 'old-call', output: `${SCENARIO_MARKER}stale` }] },
    { contents: [{ role: 'user', parts: [{ text: `${SCENARIO_MARKER}current` }] }, { role: 'user', parts: [{ functionResponse: { name: 'old-call', response: { output: `${SCENARIO_MARKER}stale` } } }] }] },
  ])('does not let model replies or tool outputs choose another scenario: %j', (body) => {
    expect(selectScenarioID(body)).toBe('current')
  })

  it('keeps direct Cursor prompts and Responses string input without scanning root metadata', () => {
    expect(selectScenarioID(`Run.\n${SCENARIO_MARKER}cursor-direct`)).toBe('cursor-direct')
    expect(selectScenarioID({ input: `Run.\n${SCENARIO_MARKER}response-direct`, metadata: { title: `${SCENARIO_MARKER}foreign` } })).toBe('response-direct')
  })

  it('falls back to the ambient scenario when no marker is present', () => {
    expect(selectScenarioID({ messages: [{ role: 'user', content: 'No marker' }] })).toBe(AMBIENT_SCENARIO_ID)
  })

  // Goose 1.53.0 `/compact` quotes the conversation in the system prompt.
  // Its only user text is a fixed instruction that holds no marker.
  it.each([
    { messages: [{ role: 'system', content: `**Conversation History:**\n[user]: Keep it.\n\n${SCENARIO_MARKER}quoted-history\n[assistant]: Kept.` }, { role: 'user', content: 'Please summarize the conversation history provided in the system prompt.' }] },
    { system: [{ type: 'text', text: `History: ${SCENARIO_MARKER}quoted-history` }], messages: [{ role: 'user', content: 'Summarize the history.' }] },
    { instructions: `History: ${SCENARIO_MARKER}quoted-history`, input: [{ role: 'user', content: [{ type: 'input_text', text: 'Summarize the history.' }] }] },
    { systemInstruction: { parts: [{ text: `History: ${SCENARIO_MARKER}quoted-history` }] }, contents: [{ role: 'user', parts: [{ text: 'Summarize the history.' }] }] },
  ])('reads the system text when no user text holds a marker: %j', (body) => {
    expect(selectScenarioID(body)).toBe('quoted-history')
  })

  it('takes the newest system text marker when the system text quotes several prompts', () => {
    const body = { messages: [{ role: 'system', content: `[user]: ${SCENARIO_MARKER}older\n[user]: ${SCENARIO_MARKER}newer` }, { role: 'user', content: 'Summarize the history.' }] }
    expect(selectScenarioID(body)).toBe('newer')
  })

  it('prefers a user text marker to a system text marker', () => {
    const body = { messages: [{ role: 'system', content: `Earlier: ${SCENARIO_MARKER}stale` }, { role: 'user', content: `Run.\n\n${SCENARIO_MARKER}current` }] }
    expect(selectScenarioID(body)).toBe('current')
  })

  it('does not let model replies or tool outputs choose the scenario when only they hold a marker', () => {
    const body = { messages: [{ role: 'system', content: 'No marker.' }, { role: 'assistant', content: `${SCENARIO_MARKER}reply` }, { role: 'tool', content: `${SCENARIO_MARKER}tool` }, { role: 'user', content: 'No marker' }] }
    expect(selectScenarioID(body)).toBe(AMBIENT_SCENARIO_ID)
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
    expect(() => parseScenarioSpec({ steps: [{}] })).toThrow('needs text, reasoning, toolCalls, or error')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', error: { status: 500, message: 'b' } }] }))
      .toThrow('cannot combine an error with output')
  })

  it('validates a tool call and an error', () => {
    expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: '', name: 'Read', arguments: {} }] }] }))
      .toThrow('needs id and name')
    expect(() => parseScenarioSpec({ steps: [{ error: { status: 200, message: 'ok' } }] }))
      .toThrow('needs an HTTP status and message')
  })

  it('keeps the mid-stream flag of an error and refuses a flag that is not a boolean', () => {
    expect(parseScenarioSpec({ steps: [{ error: { status: 500, message: 'broke', midStream: true } }] }).steps[0]?.error)
      .toEqual({ status: 500, message: 'broke', midStream: true })
    expect(parseScenarioSpec({ steps: [{ error: { status: 500, message: 'broke', midStream: false } }] }).steps[0]?.error)
      .toEqual({ status: 500, message: 'broke' })
    expect(parseScenarioSpec({ steps: [{ error: { status: 500, message: 'broke' } }] }).steps[0]?.error)
      .toEqual({ status: 500, message: 'broke' })
    expect(() => parseScenarioSpec({ steps: [{ error: { status: 500, message: 'broke', midStream: 'yes' } }] }))
      .toThrow('midStream must be a boolean')
  })

  it('takes a tool call with JSON arguments or with raw custom-tool input', () => {
    expect(parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'a', name: 'Read', arguments: { path: 'x' } }] }] }).steps[0])
      .toEqual({ toolCalls: [{ id: 'a', name: 'Read', arguments: { path: 'x' } }] })
    expect(parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'a', name: 'exec', input: 'await tools.exec_command({})' }] }] }).steps[0])
      .toEqual({ toolCalls: [{ id: 'a', name: 'exec', input: 'await tools.exec_command({})' }] })
  })

  it('preserves a provider-service completion gate separately from native arguments', () => {
    const tool = { id: 'child', name: 'task', arguments: { prompt: 'Keep the child active.' }, completionGate: 'child-completion' }
    expect(parseScenarioSpec({ steps: [{ toolCalls: [tool] }] }).steps[0]?.toolCalls).toEqual([tool])
  })

  for (const completionGate of ['', '../escape', null, 0]) {
    it(`rejects an invalid provider-service completion gate ${JSON.stringify(completionGate)}`, () => {
      expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'child', name: 'task', arguments: {}, completionGate }] }] })).toThrow(/gate/)
    })
  }

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
  it('keeps each Unicode code point intact before a native protobuf encoder sees it', () => {
    expect(textChunks({ text: 'A🚀한B', stream: { chunkChars: 1, delayMs: 0 } })).toEqual(['A', '🚀', '한', 'B'])
  })
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
  it('rejects a release gate after the final Unicode character chunk', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: '🚀', stream: { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 2, name: 'unreachable-unicode' }] } }] })).toThrow('exceeds the emitted chunk count')
  })
  it('preserves ordered gates after emitted reasoning and text chunks', () => {
    const stream = { chunkChars: 2, delayMs: 0, gates: [{ afterChunk: 1, name: 'thinking' }, { afterChunk: 3, name: 'answer' }] }
    expect(parseScenarioSpec({ steps: [{ reasoning: 'abcd', text: 'efgh', stream }] }).steps[0]?.stream).toEqual(stream)
  })

  it('accepts a stream with reasoning and no text', () => {
    const stream = { chunkChars: 2, delayMs: 0, gates: [{ afterChunk: 1, name: 'thinking' }] }
    expect(parseScenarioSpec({ steps: [{ reasoning: 'abcd', stream }] }).steps[0]).toEqual({ reasoning: 'abcd', stream })
  })

  for (const gates of [
    null,
    {},
    [{ afterChunk: 0, name: 'zero' }],
    [{ afterChunk: -1, name: 'negative' }],
    [{ afterChunk: 1.5, name: 'fraction' }],
    [{ afterChunk: 1, name: '' }],
    [{ afterChunk: 1, name: '../escape' }],
    [{ afterChunk: 2, name: 'later' }, { afterChunk: 1, name: 'earlier' }],
    [{ afterChunk: 1, name: 'one' }, { afterChunk: 1, name: 'two' }],
    [{ afterChunk: 1, name: 'same' }, { afterChunk: 2, name: 'same' }],
    [{ afterChunk: Number.MAX_SAFE_INTEGER + 1, name: 'large' }],
    [{ afterChunk: 5, name: 'unreachable' }],
  ]) {
    it(`rejects invalid stream gates ${JSON.stringify(gates)}`, () => {
      expect(() => parseScenarioSpec({ steps: [{ text: 'abcd', stream: { chunkChars: 1, delayMs: 0, gates } }] })).toThrow(/stream gate/)
    })
  }

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

describe('parseScenarioSpec gate', () => {
  it('keeps provider-service progress outside native tool arguments', () => {
    const tool = { id: 'task-1', name: 'Task', arguments: { prompt: 'Read the file.' }, taskProgress: 'The child read the file.' }
    expect(parseScenarioSpec({ steps: [{ toolCalls: [tool] }] }).steps[0]?.toolCalls?.[0]).toEqual(tool)
  })

  it('rejects an empty or non-text provider-service progress value', () => {
    for (const taskProgress of ['', 0, null, {}]) {
      expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'task-1', name: 'Task', arguments: {}, taskProgress }] }] }))
        .toThrow('taskProgress must be a non-empty string')
    }
  })

  it('keeps a release gate on a scripted answer', () => {
    expect(parseScenarioSpec({ steps: [{ text: 'Answer.', gate: 'child-answer' }] }).steps[0])
      .toEqual({ text: 'Answer.', gate: 'child-answer' })
  })

  it('rejects a missing, malformed, or timed release gate', () => {
    for (const gate of ['', 'bad/name', 'a b'])
      expect(() => parseScenarioSpec({ steps: [{ text: 'Answer.', gate }] })).toThrow('gate')
    expect(() => parseScenarioSpec({ steps: [{ text: 'Answer.', gate: 'child-answer', delayMs: 5 }] }))
      .toThrow('gate')
  })
})

describe('parseScenarioSpec captures', () => {
  const planWrite = { id: 'w', name: 'Write', arguments: { path: '{{planFile}}', content: '# Plan' } }

  it('keeps a capture that a placeholder uses', () => {
    const spec = parseScenarioSpec({ steps: [{ toolCalls: [planWrite], captures: { planFile: 'Plan file: (\\S+)' } }] })
    expect(spec.steps[0]!.captures).toEqual({ planFile: 'Plan file: (\\S+)' })
  })

  it('refuses a placeholder that no capture declares', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'Wrote {{planFile}} and {{other}}.', captures: { planFile: '(x)' } }] }))
      .toThrow('uses {{other}} but declares no capture for it')
  })

  it('refuses a capture that no placeholder uses', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'No placeholder.', captures: { planFile: '(x)' } }] }))
      .toThrow('declares capture planFile but no {{planFile}} placeholder uses it')
  })

  it('refuses an empty map, a bad name, an empty pattern, and an invalid pattern', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', captures: {} }] }))
      .toThrow('captures must be an object with at least one entry')
    expect(() => parseScenarioSpec({ steps: [{ text: '{{1st}}', captures: { '1st': '(x)' } }] }))
      .toThrow('capture name 1st must be an identifier')
    expect(() => parseScenarioSpec({ steps: [{ text: '{{a}}', captures: { a: '' } }] }))
      .toThrow('capture a must be a non-empty pattern')
    expect(() => parseScenarioSpec({ steps: [{ text: '{{a}}', captures: { a: '(' } }] }))
      .toThrow('capture a is not a valid regular expression')
  })

  it('requires exactly one capture group, so the value is never ambiguous', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: '{{a}}', captures: { a: 'x' } }] }))
      .toThrow('must declare exactly one capture group, not 0')
    expect(() => parseScenarioSpec({ steps: [{ text: '{{a}}', captures: { a: '(x)(y)' } }] }))
      .toThrow('must declare exactly one capture group, not 2')
    // A non-capturing group does not count.
    expect(() => parseScenarioSpec({ steps: [{ text: '{{a}}', captures: { a: '(?:x)(y)' } }] })).not.toThrow()
  })

  it('refuses captures on an error step, which has no text to fill', () => {
    expect(() => parseScenarioSpec({ steps: [{ error: { status: 500, message: 'boom' }, captures: { a: '(x)' } }] }))
      .toThrow('cannot combine captures with an error')
  })
})

describe('parseScenarioSpec usage and rateLimits', () => {
  it('keeps a stated usage block and leaves every absent count at the default', () => {
    const spec = parseScenarioSpec({ steps: [{ text: 'ok', usage: { inputTokens: 12000 } }] })
    expect(spec.steps[0]!.usage).toEqual({ inputTokens: 12000 })
  })

  it('keeps a rateLimits surface with only the required fields', () => {
    const spec = parseScenarioSpec({
      steps: [{ text: 'ok', rateLimits: { type: 'five_hour', status: 'exceeded' } }],
    })
    expect(spec.steps[0]!.rateLimits).toEqual({ type: 'five_hour', status: 'exceeded' })
  })

  it('keeps utilization and resetsAt when the step states them', () => {
    const spec = parseScenarioSpec({
      steps: [{
        text: 'ok',
        usage: { inputTokens: 50, outputTokens: 7, contextWindow: 200000 },
        rateLimits: { type: 'weekly', status: 'allowed', utilization: 0.4, resetsAt: 1893456000 },
      }],
    })
    expect(spec.steps[0]!.usage).toEqual({ inputTokens: 50, outputTokens: 7, contextWindow: 200000 })
    expect(spec.steps[0]!.rateLimits).toEqual({ type: 'weekly', status: 'allowed', utilization: 0.4, resetsAt: 1893456000 })
  })

  it('refuses a negative or fractional token count', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', usage: { inputTokens: -1 } }] }))
      .toThrow('usage inputTokens must be a non-negative integer')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', usage: { outputTokens: 1.5 } }] }))
      .toThrow('usage outputTokens must be a non-negative integer')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', usage: { contextWindow: Number.NaN } }] }))
      .toThrow('usage contextWindow must be a non-negative integer')
  })

  it('refuses a rateLimits object that misses type or status', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', rateLimits: { status: 'allowed' } }] }))
      .toThrow('rateLimits type must be a non-empty string')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', rateLimits: { type: 'five_hour' } }] }))
      .toThrow('rateLimits status must be a non-empty string')
  })

  it('refuses a utilization outside 0 to 1 and a negative resetsAt', () => {
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', rateLimits: { type: 't', status: 's', utilization: 1.5 } }] }))
      .toThrow('rateLimits utilization must be a number from 0 to 1')
    expect(() => parseScenarioSpec({ steps: [{ text: 'a', rateLimits: { type: 't', status: 's', resetsAt: -1 } }] }))
      .toThrow('rateLimits resetsAt must be a non-negative integer')
  })

  it('rejects nonfinite utilization and a reset time that cannot form a native date', () => {
    for (const utilization of [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY]) {
      expect(() => parseScenarioSpec({ steps: [{ text: 'A quota answer.', rateLimits: { type: 'premium_interactions', status: 'allowed', utilization } }] })).toThrow('utilization')
    }
    for (const resetsAt of [Number.MAX_SAFE_INTEGER, 8_640_000_000_001]) {
      expect(() => parseScenarioSpec({ steps: [{ text: 'A quota answer.', rateLimits: { type: 'premium_interactions', status: 'allowed', resetsAt } }] })).toThrow('resetsAt')
    }
  })

  it('refuses usage or rateLimits on an error step, which carries no answer', () => {
    expect(() => parseScenarioSpec({ steps: [{ error: { status: 500, message: 'boom' }, usage: { inputTokens: 1 } }] }))
      .toThrow('cannot combine usage with an error')
    expect(() => parseScenarioSpec({ steps: [{ error: { status: 500, message: 'boom' }, rateLimits: { type: 't', status: 's' } }] }))
      .toThrow('cannot combine rateLimits with an error')
  })
})

describe('resolveStepCaptures', () => {
  const body = {
    messages: [
      { role: 'system', content: 'You are an agent.' },
      { role: 'user', content: [{ type: 'text', text: 'Plan file: /home/a/plans/old.md' }] },
      { role: 'user', content: 'Plan file: /home/a/plans/new-plan.md\nThen write.' },
    ],
  }

  it('returns a step without captures unchanged, literal braces included', () => {
    const step = { text: 'Keep {{this}} literal.' }
    expect(resolveStepCaptures(step, body)).toEqual({ step })
  })

  it('fills text, reasoning, and every tool-call string from the last match', () => {
    const resolved = resolveStepCaptures({
      reasoning: 'Write to {{planFile}}.',
      text: 'The plan lives at {{planFile}}.',
      toolCalls: [
        { id: 'w', name: 'Write', arguments: { path: '{{planFile}}', nested: [{ note: 'see {{planFile}}' }], count: 2 } },
        { id: 'c', name: 'exec', input: 'cat {{planFile}}' },
      ],
      captures: { planFile: 'Plan file: (\\S+)' },
    }, body)
    expect(resolved).toEqual({
      step: {
        reasoning: 'Write to /home/a/plans/new-plan.md.',
        text: 'The plan lives at /home/a/plans/new-plan.md.',
        toolCalls: [
          { id: 'w', name: 'Write', arguments: { path: '/home/a/plans/new-plan.md', nested: [{ note: 'see /home/a/plans/new-plan.md' }], count: 2 } },
          { id: 'c', name: 'exec', input: 'cat /home/a/plans/new-plan.md' },
        ],
      },
    })
  })

  it('matches the raw string, not its JSON escape, so a backslash path survives', () => {
    const resolved = resolveStepCaptures(
      { text: '{{path}}', captures: { path: 'Plan file: (\\S+)' } },
      { messages: [{ role: 'user', content: 'Plan file: C:\\plans\\a.md' }] },
    )
    expect(resolved).toEqual({ step: { text: 'C:\\plans\\a.md' } })
  })

  it('reports the capture that matched nothing', () => {
    expect(resolveStepCaptures({ text: '{{planFile}}', captures: { planFile: 'Plan file: (\\S+)' } }, { messages: [] }))
      .toEqual({ unmatchedCapture: 'planFile' })
  })

  it('accepts an empty capture group as a real value', () => {
    expect(resolveStepCaptures({ text: '[{{v}}]', captures: { v: 'value=(\\w*);' } }, { note: 'value=;' }))
      .toEqual({ step: { text: '[]' } })
  })

  // The parser accepts a placeholder in every string of a tool call, so the
  // resolution must fill every one of them. Otherwise the braces reach the agent.
  it('fills a placeholder in the id, the name, and the namespace of a tool call', () => {
    const spec = parseScenarioSpec({
      steps: [{
        toolCalls: [{ id: 'call-{{n}}', name: 'tool_{{n}}', namespace: 'ns_{{n}}', arguments: { value: 1 } }],
        captures: { n: 'Number: (\\d+)' },
      }],
    })
    expect(resolveStepCaptures(spec.steps[0]!, { note: 'Number: 42' })).toEqual({
      step: { toolCalls: [{ id: 'call-42', name: 'tool_42', namespace: 'ns_42', arguments: { value: 1 } }] },
    })
  })

  // A rule answers again and again, so each resolution must leave the step that
  // the scenario keeps as the script stated it.
  it('leaves the step that it resolves unchanged, so a repeated rule resolves again', () => {
    const step = {
      text: 'At {{planFile}}.',
      toolCalls: [{ id: 'w', name: 'Write', arguments: { path: '{{planFile}}' } }],
      captures: { planFile: 'Plan file: (\\S+)' },
    }
    const before = structuredClone(step)
    expect(resolveStepCaptures(step, { note: 'Plan file: /a.md' })).toMatchObject({ step: { text: 'At /a.md.' } })
    expect(step).toEqual(before)
    expect(resolveStepCaptures(step, { note: 'Plan file: /b.md' })).toMatchObject({ step: { text: 'At /b.md.' } })
  })

  it('reports the first capture that matched nothing, when another one matched', () => {
    expect(resolveStepCaptures(
      { text: '{{found}} {{missing}}', captures: { found: 'a=(\\w+)', missing: 'b=(\\w+)' } },
      { note: 'a=1' },
    )).toEqual({ unmatchedCapture: 'missing' })
  })

  it('refuses a request body that holds no string at all', () => {
    expect(resolveStepCaptures({ text: '{{v}}', captures: { v: '(x)' } }, null)).toEqual({ unmatchedCapture: 'v' })
    expect(resolveStepCaptures({ text: '{{v}}', captures: { v: '(x)' } }, { count: 1, flag: true })).toEqual({ unmatchedCapture: 'v' })
  })
})

describe('native rule priority', () => {
  it.each(['high', 'normal'])('preserves the explicit %s priority', (priority) => {
    const scenario = parseScenarioSpec({ rules: [{ name: 'priority-rule', priority, when: {}, respond: { text: 'A rule answer.' } }] })
    expect(scenario.rules[0]).toHaveProperty('priority', priority)
  })

  it.each([null, '', false, 0, 'urgent', {}, []].map(priority => ({ priority })))('rejects an invalid native rule priority: $priority', ({ priority }) => {
    expect(() => parseScenarioSpec({ rules: [{ name: 'priority-rule', priority, when: {}, respond: { text: 'A rule answer.' } }] })).toThrow('priority')
  })
})

describe('native child execution metadata', () => {
  it('preserves the selected native model outside the tool arguments', () => {
    const scenario = parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'native-child', name: 'task', arguments: { prompt: 'The actual child task.' }, nativeExecution: { modelId: 'default' } }] }] })
    expect(scenario.steps[0]?.toolCalls?.[0]).toEqual({ id: 'native-child', name: 'task', arguments: { prompt: 'The actual child task.' }, nativeExecution: { modelId: 'default' } })
  })

  it.each([null, '', false, 0, [], {}, { modelId: null }, { modelId: '' }, { modelId: 0 }].map(nativeExecution => ({ nativeExecution })))('rejects invalid native execution metadata: $nativeExecution', ({ nativeExecution }) => {
    expect(() => parseScenarioSpec({ steps: [{ toolCalls: [{ id: 'native-child', name: 'task', arguments: { prompt: 'Read.' }, nativeExecution }] }] })).toThrow('nativeExecution')
  })
})

function parsedLastMessageMatcher(value: unknown = { role: 'system', text: ['^Background task completed\\.', 'description: Current child(?:\\n|$)'] }) {
  return parseScenarioSpec({ steps: [], rules: [{ name: 'current native message', when: { lastMessage: value }, respond: { text: 'The child completed.' } }] }).rules[0]!.when
}
function lastMessageRequest(body: unknown, protocol: 'openai-chat-completions' | 'openai-responses' | 'anthropic-messages' = 'openai-chat-completions') {
  return { protocol, body, systemText: '', userText: '' }
}
const completion = { role: 'system', content: 'Background task completed.\ntask_id: native-id\ndescription: Current child\noutput: CHILD_DONE' }

describe('matchesRequest lastMessage', () => {
  it('retains declared native message criteria during parsing', () => {
    expect(parsedLastMessageMatcher()).toMatchObject({ lastMessage: { role: 'system', text: ['^Background task completed\\.', 'description: Current child(?:\\n|$)'] } })
  })
  it('matches the current native system completion and rejects old history', () => {
    expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest({ messages: [completion] }))).toBe(true)
    expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest({ messages: [completion, { role: 'user', content: 'Start the next child.' }] }))).toBe(false)
  })
  it('reads current Responses input blocks and Anthropic messages', () => {
    const blocks = { role: 'system', content: [{ type: 'input_text', text: completion.content }] }
    expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest({ input: [blocks] }, 'openai-responses'))).toBe(true)
    expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest({ messages: [completion] }, 'anthropic-messages'))).toBe(true)
  })
  it('rejects a wrong role, another child, and tool-schema text', () => {
    for (const body of [
      { messages: [{ ...completion, role: 'user' }] },
      { messages: [{ ...completion, content: completion.content.replace('Current child', 'Other child') }] },
      { messages: [{ role: 'user', content: 'Continue.' }], tools: [completion] },
    ]) expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest(body))).toBe(false)
  })
  it('rejects absent, empty, malformed, and nontext final messages', () => {
    for (const body of [undefined, {}, { messages: [] }, { messages: [completion, null] }, { messages: [completion, 42] }, { messages: [completion, {}] }, { messages: [completion, { role: 'system', content: [] }] }, { messages: [completion, { role: 'system', content: [{ type: 'image_url', image_url: { url: completion.content } }] }] }])
      expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest(body))).toBe(false)
  })
  it('uses the protocol array without falling back to stale alternate history', () => {
    expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest({ messages: [], input: [completion] }))).toBe(false)
    expect(matchesRequest(parsedLastMessageMatcher(), lastMessageRequest({ messages: [completion], input: [] }, 'openai-responses'))).toBe(false)
  })
  it('rejects invalid criteria before a model turn', () => {
    for (const criteria of [null, [], '', {}, { role: '' }, { role: 'invalid' }, { role: 1 }, { text: '' }, { text: [] }, { text: ['valid', ''] }, { text: '[' }, { text: 2 }, { role: 'system', unknown: true }])
      expect(() => parsedLastMessageMatcher(criteria)).toThrow()
  })
})

describe('stepRequest', () => {
  function status(overrides: Partial<MockModelScenarioStatus> = {}): MockModelScenarioStatus {
    return {
      complete: false,
      nextStep: 3,
      stepCount: 4,
      ruleMatches: {},
      pendingGates: [],
      requests: [
        { protocol: 'openai-chat-completions', path: '/v1/chat/completions', rule: 'title', body: { marker: 'rule' } },
        { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex: 1, body: { marker: 'one' } },
        { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex: 2, body: { marker: 'two' } },
      ],
      unexpectedRequests: [],
      ...overrides,
    }
  }

  it('returns the record of the requested step, not a neighbor or a rule answer', () => {
    expect(stepRequest(status(), 1).body).toEqual({ marker: 'one' })
    expect(stepRequest(status(), 2).body).toEqual({ marker: 'two' })
  })

  it('states that the agent did not request a later step, with the script state', () => {
    expect(() => stepRequest(status(), 3)).toThrow(
      'The model script holds no request for step 3: the agent did not request it; 3 of 4 queued answers consumed, 0 requests the script did not answer.',
    )
  })

  it('states that the record cap dropped a consumed step', () => {
    expect(() => stepRequest(status(), 0)).toThrow(
      `step 0: the agent requested it, but the server keeps only the newest ${MAX_SCENARIO_REQUEST_RECORDS} request records`,
    )
  })

  it('counts the unexpected requests in its message', () => {
    const unexpected = { protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', reason: 'no answer', body: {} }
    expect(() => stepRequest(status({ unexpectedRequests: [unexpected] }), 5)).toThrow('1 request the script did not answer')
  })

  it('fails for an empty status', () => {
    expect(() => stepRequest(status({ nextStep: 0, stepCount: 0, requests: [] }), 0))
      .toThrow('step 0: the agent did not request it; 0 of 0 queued answers consumed')
  })

  it.each([-1, 1.5, Number.NaN, Number.MAX_SAFE_INTEGER, Number.POSITIVE_INFINITY])('rejects the index %s, which identifies no step', (index) => {
    expect(() => stepRequest(status(), index)).toThrow('A model script step index must be a nonnegative safe integer')
  })
})

describe('validateStepIndex', () => {
  it.each([0, 1, Number.MAX_SAFE_INTEGER - 1])('accepts the index %s, whose next step count stays safe', (index) => {
    expect(() => validateStepIndex(index)).not.toThrow()
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER + 1])('rejects the index %s', (index) => {
    expect(() => validateStepIndex(index)).toThrow(`A model script step index must be a nonnegative safe integer, not ${index}.`)
  })
})

describe('describeScenarioStatus', () => {
  it('states the consumed count and the unexpected requests in one line', () => {
    const base = { complete: false, ruleMatches: {}, pendingGates: [], requests: [] }
    expect(describeScenarioStatus({ ...base, nextStep: 1, stepCount: 2, unexpectedRequests: [] }))
      .toBe('1 of 2 queued answers consumed, 0 requests the script did not answer')
    const unexpected = { protocol: 'openai-responses' as const, path: '/v1/responses', reason: 'no answer', body: {} }
    expect(describeScenarioStatus({ ...base, nextStep: 0, stepCount: 0, unexpectedRequests: [unexpected] }))
      .toBe('0 of 0 queued answers consumed, 1 request the script did not answer')
  })
})

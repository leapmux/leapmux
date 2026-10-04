import type { MockModelRequestRecord } from './mockModelScript'
import { describe, expect, it, vi } from 'vitest'
import { nativeLastStepBody, nativeModelBodiesAfter, nativeModelContextText, nativeModelInstructionText, nativeModelLastUserText, nativeModelToolNames, nativeScenarioModelContextText } from './nativeScenario'

describe('nativeScenarioModelContextText', () => {
  it('uses the injected reader for the unchanged recorded request', () => {
    const request: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', body: { metadata: 'GENERIC_ONLY' } }
    const reader = vi.fn(() => 'PROVIDER_CONTEXT')
    expect(nativeScenarioModelContextText({ readModelContext: reader }, request)).toBe('PROVIDER_CONTEXT')
    expect(reader).toHaveBeenCalledExactlyOnceWith(request)
    expect(request.body).toEqual({ metadata: 'GENERIC_ONLY' })
  })

  it('preserves an empty injected result and its original failure', () => {
    const request: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', body: { metadata: 'GENERIC_ONLY' } }
    expect(nativeScenarioModelContextText({ readModelContext: () => '' }, request)).toBe('')
    const failure = new Error('The provider context reader failed.')
    expect(() => nativeScenarioModelContextText({ readModelContext: () => {
      throw failure
    } }, request)).toThrow(failure)
  })

  it('keeps generic server-held context when the scenario supplies no reader', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-responses', path: '/responses', body: { prompt: 'CURRENT_PROMPT' }, serverContext: { conversationId: 'native-service', messages: [{ role: 'user', content: 'PRIOR_PROMPT' }, { role: 'assistant', content: 'PRIOR_ANSWER' }] } }
    expect(nativeScenarioModelContextText({}, request)).toBe(nativeModelContextText(request))
    expect(nativeScenarioModelContextText({}, request)).toContain('PRIOR_ANSWER')
  })
})

describe('nativeModelBodiesAfter', () => {
  it('keeps the inclusive step index and excludes unqueued requests', () => {
    const requests = [{ stepIndex: 0, body: { selected: 'before' } }, { stepIndex: 1, body: { selected: 'first included' } }, { body: { selected: 'unqueued' } }, { stepIndex: 2, body: { selected: 'last included' } }]
    expect(nativeModelBodiesAfter({ requests }, 1)).toBe('{"selected":"first included"}\n{"selected":"last included"}')
    expect(requests).toHaveLength(4)
    expect(requests[0]?.body).toEqual({ selected: 'before' })
  })

  it('keeps step zero and native scalar values', () => {
    expect(nativeModelBodiesAfter({ requests: [{ stepIndex: 0, body: { zero: 0, disabled: false, empty: '', nullable: null } }] }, 0)).toBe('{"zero":0,"disabled":false,"empty":"","nullable":null}')
  })

  it('returns an empty string when no queued request reaches the requested step', () => {
    expect(nativeModelBodiesAfter({ requests: [] }, 0)).toBe('')
    expect(nativeModelBodiesAfter({ requests: [{ body: { ignored: true } }, { stepIndex: 0, body: {} }] }, 1)).toBe('')
  })
})

describe('nativeLastStepBody', () => {
  it('keeps step zero and native values in a shallow copy', () => {
    const nested = { value: 'nested' }
    const body = { model: 'queued-model', tokens: 0, enabled: false, text: '', nullable: null, nested }
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 0, body }
    const result = nativeLastStepBody([request])
    expect(result).toEqual(body)
    expect(result).not.toBe(body)
    expect(result.nested).toBe(nested)
    result.model = 'changed-copy'
    expect(body.model).toBe('queued-model')
    expect(request.body).toBe(body)
  })

  it('selects the last queued request and ignores later rule and fallback requests', () => {
    const requests: MockModelRequestRecord[] = [
      { protocol: 'aws-event-stream', path: '/', stepIndex: 0, body: { selected: 'first' } },
      { protocol: 'aws-event-stream', path: '/', stepIndex: 1, body: { selected: 'last queued' } },
      { protocol: 'aws-event-stream', path: '/', rule: 'independent native rule', body: { selected: 'rule' } },
      { protocol: 'aws-event-stream', path: '/', fallback: true, body: { selected: 'fallback' } },
    ]
    expect(nativeLastStepBody(requests)).toEqual({ selected: 'last queued' })
  })

  it('rejects an empty list or a list without a queued request', () => {
    expect(() => nativeLastStepBody([])).toThrow('The script contains no valid queued model request body.')
    expect(() => nativeLastStepBody([{ protocol: 'openai-responses', path: '/responses', fallback: true, body: { ignored: true } }])).toThrow('The script contains no valid queued model request body.')
  })

  it.each([undefined, null, false, 0, '', 'body text'])('rejects an absent or primitive queued body: %j', (body) => {
    expect(() => nativeLastStepBody([{ protocol: 'anthropic-messages', path: '/v1/messages', stepIndex: 0, body }])).toThrow('The script contains no valid queued model request body.')
  })

  it.each([{ body: [] }, { body: ['not a request object'] }])('rejects an array queued body: $body', ({ body }) => {
    expect(() => nativeLastStepBody([{ protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 0, body }])).toThrow('The script contains no valid queued model request body.')
  })
})

describe('nativeModelContextText', () => {
  it('keeps the current prompt when server history is empty', () => {
    const request: MockModelRequestRecord = {
      protocol: 'openai-responses',
      path: '/responses',
      body: { prompt: 'CURRENT_PROMPT_MARKER' },
      serverContext: { conversationId: 'conversation-1', messages: [] },
    }
    expect(nativeModelContextText(request)).toContain('CURRENT_PROMPT_MARKER')
  })

  it('keeps the current prompt and the prior completed exchange', () => {
    const request: MockModelRequestRecord = {
      protocol: 'openai-responses',
      path: '/responses',
      body: { prompt: 'NEXT_PROMPT_MARKER' },
      serverContext: {
        conversationId: 'conversation-1',
        messages: [
          { role: 'user', content: 'PRIOR_PROMPT_MARKER' },
          { role: 'assistant', content: 'PRIOR_ANSWER_MARKER' },
        ],
      },
    }
    const text = nativeModelContextText(request)
    expect(text).toContain('NEXT_PROMPT_MARKER')
    expect(text).toContain('PRIOR_PROMPT_MARKER')
    expect(text).toContain('PRIOR_ANSWER_MARKER')
    expect(request.body).toEqual({ prompt: 'NEXT_PROMPT_MARKER' })
  })

  it('keeps native body values when there is no server history', () => {
    const body = { prompt: 'NATIVE_BODY_MARKER', enabled: false, usedTokens: 0, emptyText: '', nullable: null }
    const request: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', body }
    const text = nativeModelContextText(request)
    expect(text).toContain('NATIVE_BODY_MARKER')
    expect(text).toContain('"enabled":false')
    expect(text).toContain('"usedTokens":0')
    expect(text).toContain('"emptyText":""')
    expect(text).toContain('"nullable":null')
    expect(request.body).toBe(body)
  })
})

describe('nativeModelToolNames', () => {
  it('reads Google function declarations in native order without changing them', () => {
    const body = { tools: [{ functionDeclarations: [{ name: 'read_file' }, { name: 'run_shell_command' }] }, { functionDeclarations: [{ name: 'read_file' }] }] }
    expect(nativeModelToolNames({ protocol: 'google-generative-language', path: '/v1beta/models/gemini-2.5-pro:generateContent', body })).toEqual(['read_file', 'run_shell_command', 'read_file'])
    expect(body.tools[0]?.functionDeclarations[0]?.name).toBe('read_file')
  })

  it('reads the captured standard custom tool without dropping ordinary catalog entries', () => {
    const tools = [
      { type: 'function', function: { name: 'bash' } },
      { type: 'custom', custom: { name: 'apply_patch' } },
      { name: 'view' },
    ]
    const before = JSON.stringify(tools)
    expect(nativeModelToolNames({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { tools } }))
      .toEqual(['bash', 'apply_patch', 'view'])
    expect(JSON.stringify(tools)).toBe(before)
  })

  it.each([
    undefined,
    null,
    [],
    'apply_patch',
    0,
    false,
    {},
    { name: undefined },
    { name: null },
    { name: '' },
    { name: 0 },
    { name: false },
    { name: [] },
  ])('refuses an absent or malformed custom catalog object: %j', (custom) => {
    expect(() => nativeModelToolNames({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { tools: [{ type: 'custom', custom }] } }))
      .toThrow(/invalid entry|without a name/)
  })

  it.each([undefined, null, '', 'function', 'namespace'])('refuses a custom-only object with another type: %j', (type) => {
    expect(() => nativeModelToolNames({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { tools: [{ type, custom: { name: 'apply_patch' } }] } }))
      .toThrow('without a name')
  })

  it('preserves catalog order and duplicate native names across ordinary and custom tools', () => {
    expect(nativeModelToolNames({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { tools: [
      { name: 'read' },
      { type: 'custom', custom: { name: 'apply_patch' } },
      { type: 'function', function: { name: 'read' } },
      { type: 'custom', custom: { name: 'apply_patch' } },
    ] } })).toEqual(['read', 'apply_patch', 'read', 'apply_patch'])
  })

  it('keeps the existing direct-name precedence and ordinary duplicate behavior', () => {
    expect(nativeModelToolNames({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { tools: [
      { name: 'direct', function: { name: 'nested' }, custom: { name: 'other' } },
      { type: 'function', function: { name: 'same' } },
      { name: 'same' },
    ] } })).toEqual(['direct', 'same', 'same'])
  })

  it('reads actual generic model tool names without changing the request', () => {
    const body = { tools: [{ name: 'anthropic_tool' }, { type: 'function', function: { name: 'chat_tool' } }, { type: 'function', name: 'responses_tool' }] }
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/chat/completions', body }
    expect(nativeModelToolNames(request)).toEqual(['anthropic_tool', 'chat_tool', 'responses_tool'])
    expect(request.body).toBe(body)
  })

  it.each([null, {}, { tools: null }, { tools: [] }, { tools: 'broken' }])('rejects a missing or empty catalog: %j', (body) => {
    expect(() => nativeModelToolNames({ protocol: 'anthropic-messages', path: '/v1/messages', body })).toThrow('no nonempty tool catalog')
  })

  it.each([null, [], 'tool', 0, false, {}, { name: '' }, { name: 0 }, { function: { name: null } }])('rejects an invalid native catalog entry: %j', (tool) => {
    expect(() => nativeModelToolNames({ protocol: 'openai-responses', path: '/responses', body: { tools: [tool] } })).toThrow(/invalid entry|without a name/)
  })
})

describe('nativeModelInstructionText', () => {
  it('reads Google instructions and user text without native function responses', () => {
    const body = {
      systemInstruction: { parts: [{ text: 'ACTUAL_SYSTEM' }] },
      contents: [
        { role: 'user', parts: [{ text: 'ACTUAL_USER' }] },
        { role: 'model', parts: [{ text: 'MODEL_ONLY' }] },
        { role: 'user', parts: [{ functionResponse: { response: { text: 'RESULT_ONLY' } } }] },
      ],
    }
    expect(nativeModelInstructionText({ protocol: 'google-generative-language', path: '/google', body })).toBe('ACTUAL_SYSTEM\nACTUAL_USER')
  })

  it('includes actual instructions and user text while excluding schemas, results, and assistant text', () => {
    const request: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', body: {
      system: [{ type: 'text', text: 'SYSTEM_INSTRUCTION' }],
      tools: [{ name: 'tool', description: 'SCHEMA_PLAN_MODE' }],
      messages: [
        { role: 'developer', content: 'DEVELOPER_INSTRUCTION' },
        { role: 'user', content: [{ type: 'text', text: 'USER_PROMPT' }, { type: 'tool_result', content: 'TOOL_RESULT_PLAN_MODE' }] },
        { role: 'assistant', content: 'ASSISTANT_PLAN_MODE' },
        { role: 'tool', content: 'TOOL_ROLE_PLAN_MODE' },
      ],
    } }
    expect(nativeModelInstructionText(request)).toBe('SYSTEM_INSTRUCTION\nDEVELOPER_INSTRUCTION\nUSER_PROMPT')
  })

  it('reads Responses instructions and input text without function output', () => {
    expect(nativeModelInstructionText({ protocol: 'openai-responses', path: '/responses', body: {
      instructions: 'RESPONSE_INSTRUCTION',
      input: [{ role: 'user', content: [{ type: 'input_text', text: 'CURRENT_PROMPT' }] }, { type: 'function_call_output', output: 'RESULT_PLAN_MODE' }],
    } })).toBe('RESPONSE_INSTRUCTION\nCURRENT_PROMPT')
  })

  it.each([null, {}, { messages: [{ role: 'assistant', content: 'Not an instruction.' }] }])('rejects absent native instruction text: %j', (body) => {
    expect(() => nativeModelInstructionText({ protocol: 'openai-chat-completions', path: '/chat/completions', body })).toThrow(/must be an object|no instruction/)
  })

  it('requires a provider-owned service instruction reader', () => {
    expect(() => nativeModelInstructionText({ protocol: 'aws-event-stream', path: '/', body: { conversationState: {} } })).toThrow('own instruction reader')
  })
})

describe('nativeModelLastUserText', () => {
  it('keeps the actual Google user prompt after function-response-only rows', () => {
    const body = { contents: [
      { role: 'user', parts: [{ text: 'EARLIER_USER' }] },
      { role: 'model', parts: [{ text: 'MODEL_ONLY' }] },
      { role: 'user', parts: [{ text: 'ACTUAL_USER 🧪' }] },
      { role: 'user', parts: [{ functionResponse: { response: { text: 'RESULT_ONLY' } } }] },
    ] }
    expect(nativeModelLastUserText({ protocol: 'google-generative-language', path: '/google', body })).toBe('ACTUAL_USER 🧪')
  })

  it('excludes earlier mode text, schemas, tool results, and assistant content', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/chat/completions', body: {
      tools: [{ name: 'tool', description: 'SCHEMA_PLAN_MODE' }],
      messages: [
        { role: 'user', content: 'EARLIER_PLAN_MODE' },
        { role: 'assistant', content: 'ASSISTANT_PLAN_MODE' },
        { role: 'user', content: [{ type: 'text', text: 'ACTUAL_CURRENT_PROMPT' }, { type: 'tool_result', text: 'RESULT_PLAN_MODE', content: 'RESULT_PLAN_MODE' }] },
        { role: 'tool', content: 'TOOL_ROLE_PLAN_MODE' },
      ],
    } }
    expect(nativeModelLastUserText(request)).toBe('ACTUAL_CURRENT_PROMPT')
  })

  it('preserves ordered Anthropic text blocks and Unicode', () => {
    expect(nativeModelLastUserText({ protocol: 'anthropic-messages', path: '/v1/messages', body: {
      messages: [{ role: 'user', content: [{ type: 'text', text: 'Actual user text.' }, { type: 'text', text: '실제 내용 🧪' }] }],
    } })).toBe('Actual user text.\n실제 내용 🧪')
  })

  it('reads the current Responses user text without a function result', () => {
    expect(nativeModelLastUserText({ protocol: 'openai-responses', path: '/responses', body: {
      input: [{ role: 'user', content: [{ type: 'input_text', text: 'CURRENT_RESPONSES_PROMPT' }] }, { type: 'function_call_output', output: 'RESULT_PLAN_MODE' }],
    } })).toBe('CURRENT_RESPONSES_PROMPT')
    expect(nativeModelLastUserText({ protocol: 'openai-responses', path: '/responses', body: { input: 'Actual direct user input.' } })).toBe('Actual direct user input.')
  })

  it.each([null, {}, { messages: null }, { messages: [] }, { messages: [{ role: 'assistant', content: 'No user.' }] }, { messages: [{ role: 'user', content: '' }] }, { messages: [{ role: 'user', content: [{ type: 'tool_result', content: 'No text.' }] }] }])('rejects absent current user text: %j', (body) => {
    expect(() => nativeModelLastUserText({ protocol: 'openai-chat-completions', path: '/chat/completions', body })).toThrow(/must be an object|no user|no last user/)
  })

  it('requires a provider-owned native service reader', () => {
    expect(() => nativeModelLastUserText({ protocol: 'aws-event-stream', path: '/', body: {} })).toThrow('own reader for the last user message')
  })
})

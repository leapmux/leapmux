import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { AgentInfoSchema, AgentStatus, AvailableOptionGroupSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import {
  currentNativeAgent,
  expectNativeOptionValue,
  nativeAgentById,
  nativeAgentsByIds,
  nativeLastStepBody,
  nativeModelBodiesAfter,
  nativeModelContextText,
  nativeModelConversationTurns,
  nativeModelInstructionText,
  nativeModelLastUserText,
  nativeModelToolNames,
  nativeOptionGroup,
  nativeOptionValue,
  nativeScenarioModelContextText,
  nativeToolArgumentText,
  nativeToolOutcome,
  selectedAgentTab,
  selectedAgentTabId,
} from './nativeScenario'

const workerChannel = vi.hoisted(() => ({ callWorker: vi.fn() }))
vi.mock('./api', () => ({ getTestChannel: async () => workerChannel }))

beforeEach(() => {
  workerChannel.callWorker.mockReset()
})

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

describe('nativeToolOutcome', () => {
  const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
    { role: 'tool', tool_call_id: 'other', content: 'OTHER_RESULT' },
    { role: 'tool', tool_call_id: 'selected', content: 'GENERIC_RESULT' },
  ] } }

  it('reads through the provider reader and keeps its failure fields', async () => {
    const reader = vi.fn(async () => ({ text: 'PROVIDER_RESULT', failed: true, exitCode: 7 }))
    expect(await nativeToolOutcome({ readToolResult: reader }, request, 'selected')).toEqual({ text: 'PROVIDER_RESULT', failed: true, exitCode: 7 })
    expect(reader).toHaveBeenCalledExactlyOnceWith(request, 'selected')
  })

  it.each([{}, { readToolResult: undefined }])('reads the result of the exact call through the generic reader without a provider reader: %j', async (context) => {
    expect(await nativeToolOutcome(context, request, 'selected')).toEqual({ text: 'GENERIC_RESULT' })
  })

  it('keeps the failure of the generic reader for an absent call', async () => {
    await expect(nativeToolOutcome({}, request, 'absent')).rejects.toThrow()
  })

  it('keeps the failure of the provider reader', async () => {
    const failure = new Error('The provider result reader failed.')
    await expect(nativeToolOutcome({ readToolResult: () => {
      throw failure
    } }, request, 'selected')).rejects.toBe(failure)
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

describe('selectedAgentTab', () => {
  it('locates the first visible selected agent tab', () => {
    const first = vi.fn(() => 'FIRST_TAB')
    const locator = vi.fn((_selector: string) => ({ first }))
    const page = Object.assign({} as Page, { locator })
    expect(selectedAgentTab(page)).toBe('FIRST_TAB')
    expect(locator).toHaveBeenCalledExactlyOnceWith('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible')
    expect(first).toHaveBeenCalledOnce()
  })
})

/** A page whose selected agent tab is visible and holds `tabId`. Its `toBeVisible` checks read the fake locator. */
function pageWithSelectedTab(tabId: string | null): Page {
  const tab: Locator = fakeLocator(undefined, {
    first: (): Locator => tab,
    getAttribute: async (name: string) => name === 'data-tab-id' ? tabId : null,
  })
  return Object.assign({} as Page, { locator: () => tab })
}

describe('selectedAgentTabId', () => {
  it('returns the agent ID of the selected tab', async () => {
    expect(await selectedAgentTabId(pageWithSelectedTab('selected-agent'))).toBe('selected-agent')
  })

  it.each([null, ''])('fails with the absent attribute instead of returning an empty agent ID: %j', async (tabId) => {
    await expect(selectedAgentTabId(pageWithSelectedTab(tabId))).rejects.toThrow('The selected agent tab has no agent ID')
  })
})

describe('nativeAgentsByIds', () => {
  const server = { leapmuxServer: { hubUrl: 'http://hub.invalid', adminToken: 'session', workerId: 'worker-1' } }

  it('reads the Worker agents of the given IDs through one ListAgents call', async () => {
    const agents = [create(AgentInfoSchema, { id: 'first' }), create(AgentInfoSchema, { id: 'second' })]
    workerChannel.callWorker.mockResolvedValueOnce({ agents })
    expect(await nativeAgentsByIds(server, ['first', 'second'])).toBe(agents)
    expect(workerChannel.callWorker).toHaveBeenCalledExactlyOnceWith('worker-1', 'ListAgents', expect.anything(), expect.anything(), { tabIds: ['first', 'second'] })
  })

  it('throws the failure of the Worker read, so a retried wait reads again and reports it', async () => {
    const failure = new Error('The Worker channel closed.')
    workerChannel.callWorker.mockRejectedValueOnce(failure)
    await expect(nativeAgentsByIds(server, ['first'])).rejects.toBe(failure)
  })

  it.each([{ label: 'no ID', ids: [] }, { label: 'an empty ID', ids: ['first', ''] }])('refuses $label before the read', async ({ ids }) => {
    await expect(nativeAgentsByIds(server, ids)).rejects.toThrow('one or more agent IDs, each nonempty')
    expect(workerChannel.callWorker).not.toHaveBeenCalled()
  })
})

describe('nativeAgentById', () => {
  const server = { leapmuxServer: { hubUrl: 'http://hub.invalid', adminToken: 'session', workerId: 'worker-1' } }

  it('returns the agent of the ID and ignores another agent in the reply', async () => {
    const agent = create(AgentInfoSchema, { id: 'wanted' })
    workerChannel.callWorker.mockResolvedValueOnce({ agents: [create(AgentInfoSchema, { id: 'other' }), agent] })
    expect(await nativeAgentById(server, 'wanted')).toBe(agent)
    expect(workerChannel.callWorker).toHaveBeenCalledWith('worker-1', 'ListAgents', expect.anything(), expect.anything(), { tabIds: ['wanted'] })
  })

  it('returns null when the Worker holds no such agent', async () => {
    workerChannel.callWorker.mockResolvedValueOnce({ agents: [] })
    expect(await nativeAgentById(server, 'missing')).toBeNull()
  })
})

describe('currentNativeAgent', () => {
  const context = { page: pageWithSelectedTab('selected-agent'), leapmuxServer: { hubUrl: 'http://hub.invalid', adminToken: 'session', workerId: 'worker-1' } }

  it('reads again after a Worker read that throws, and returns the active agent', async () => {
    const agent = create(AgentInfoSchema, { id: 'selected-agent', status: AgentStatus.ACTIVE })
    workerChannel.callWorker
      .mockRejectedValueOnce(new Error('The Worker channel closed while the Worker reconnected.'))
      .mockResolvedValue({ agents: [agent] })
    await expect(currentNativeAgent(context)).resolves.toBe(agent)
    expect(workerChannel.callWorker).toHaveBeenCalledTimes(2)
  })

  it('reads again while the agent starts, and returns it once it is active', async () => {
    const starting = create(AgentInfoSchema, { id: 'selected-agent', status: AgentStatus.STARTING })
    const active = create(AgentInfoSchema, { id: 'selected-agent', status: AgentStatus.ACTIVE })
    workerChannel.callWorker
      .mockResolvedValueOnce({ agents: [starting] })
      .mockResolvedValueOnce({ agents: [] })
      .mockResolvedValue({ agents: [active] })
    await expect(currentNativeAgent(context)).resolves.toBe(active)
    expect(workerChannel.callWorker).toHaveBeenCalledTimes(3)
  })
})

describe('nativeOptionValue', () => {
  const agent = create(AgentInfoSchema, { optionGroups: [
    create(AvailableOptionGroupSchema, { id: 'permissionMode', currentValue: 'plan', options: [{ id: 'plan', name: 'Plan' }] }),
    create(AvailableOptionGroupSchema, { id: 'effort', currentValue: '', options: [] }),
  ] })

  it('returns the current value of the group, and the group itself', () => {
    expect(nativeOptionValue(agent, 'permissionMode')).toBe('plan')
    expect(nativeOptionGroup(agent, 'permissionMode')?.options.map(option => option.id)).toEqual(['plan'])
  })

  it('keeps an empty current value apart from an absent group', () => {
    expect(nativeOptionValue(agent, 'effort')).toBe('')
    expect(nativeOptionValue(agent, 'model')).toBeUndefined()
    expect(nativeOptionGroup(agent, 'model')).toBeUndefined()
  })
})

describe('expectNativeOptionValue', () => {
  const context = { page: pageWithSelectedTab('selected-agent'), leapmuxServer: { hubUrl: 'http://hub.invalid', adminToken: 'session', workerId: 'worker-1' } }
  const agent = create(AgentInfoSchema, {
    id: 'selected-agent',
    status: AgentStatus.ACTIVE,
    optionGroups: [create(AvailableOptionGroupSchema, { id: 'permissionMode', currentValue: 'plan' })],
  })

  it('passes for the current value of the active agent', async () => {
    workerChannel.callWorker.mockResolvedValue({ agents: [agent] })
    await expect(expectNativeOptionValue(context, 'permissionMode', 'plan')).resolves.toBeUndefined()
  })

  it('fails with the group and its value for another value', async () => {
    workerChannel.callWorker.mockResolvedValue({ agents: [agent] })
    await expect(expectNativeOptionValue(context, 'permissionMode', 'build')).rejects.toThrow('the current value of the native option group permissionMode')
  })

  it('fails with the groups of the catalog for an absent group, not with "expected undefined"', async () => {
    workerChannel.callWorker.mockResolvedValue({ agents: [agent] })
    await expect(expectNativeOptionValue(context, 'model', 'mock')).rejects.toThrow('The native catalog has no option group model. It has permissionMode.')
  })
})

describe('nativeToolArgumentText', () => {
  it('reads the string values of encoded JSON arguments without keys', () => {
    expect(nativeToolArgumentText('{"KEY_ONLY":"first","nested":{"items":["second",3,false]}}')).toBe('first\nsecond')
  })

  it('reads the string values of decoded arguments', () => {
    expect(nativeToolArgumentText({ answer: 'ACTUAL_ANSWER', count: 0, nested: [{ text: 'NESTED' }] })).toBe('ACTUAL_ANSWER\nNESTED')
  })

  it.each(['not JSON {', '"a JSON string"', '42', 'true', 'null', ''])('keeps the literal text of arguments that encode no object: %j', (value) => {
    expect(nativeToolArgumentText(value)).toBe(value)
  })

  it.each([undefined, null, 0, false, {}, []])('returns no text for arguments without strings: %j', (value) => {
    expect(nativeToolArgumentText(value)).toBe('')
  })
})

describe('nativeModelConversationTurns', () => {
  it('reads Google user and model turns, including model function-call arguments', () => {
    const request: MockModelRequestRecord = { protocol: 'google-generative-language', path: '/google', body: {
      systemInstruction: { parts: [{ text: 'SYSTEM_ONLY' }] },
      contents: [
        { role: 'user', parts: [{ text: '<session_context>' }] },
        { role: 'user', parts: [{ text: 'ORIGINAL_PROMPT' }] },
        { role: 'model', parts: [{ text: 'ORIGINAL_ANSWER' }, { functionCall: { name: 'answer', args: { text: 'CALL_ARGUMENT' } } }] },
        { role: 'user', parts: [{ functionResponse: { name: 'answer', response: { output: 'RESULT_ONLY' } } }] },
        { role: 'function', parts: [{ text: 'UNKNOWN_ROLE' }] },
        'not a row',
        { role: 'user', parts: [{ text: 'RESUMED_PROMPT' }] },
      ],
    } }
    expect(nativeModelConversationTurns(request)).toEqual([
      { role: 'user', text: '<session_context>' },
      { role: 'user', text: 'ORIGINAL_PROMPT' },
      { role: 'assistant', text: 'ORIGINAL_ANSWER\nCALL_ARGUMENT' },
      { role: 'user', text: '' },
      { role: 'user', text: 'RESUMED_PROMPT' },
    ])
  })

  it('excludes Google thought parts and keeps a model turn without parts', () => {
    const request: MockModelRequestRecord = { protocol: 'google-generative-language', path: '/google', body: {
      contents: [
        { role: 'model', parts: [{ text: 'THOUGHT_ONLY', thought: true }, { functionCall: { args: { text: 'THOUGHT_CALL' } }, thought: true }, { text: 'ANSWER_TEXT', thought: false }] },
        { role: 'model' },
      ],
    } }
    expect(nativeModelConversationTurns(request)).toEqual([
      { role: 'assistant', text: 'ANSWER_TEXT' },
      { role: 'assistant', text: '' },
    ])
  })

  it('reads Responses messages and tool calls without outputs, reasoning, or instructions', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-responses', path: '/responses', body: {
      instructions: 'INSTRUCTION_ONLY',
      input: [
        { role: 'developer', content: 'DEVELOPER_ONLY' },
        { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'ORIGINAL_PROMPT' }, { type: 'input_image', image_url: 'IMAGE_ONLY' }] },
        { type: 'reasoning', summary: [{ type: 'summary_text', text: 'REASONING_ONLY' }] },
        { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'ORIGINAL_ANSWER' }] },
        { type: 'function_call', name: 'answer', arguments: '{"text":"CALL_ARGUMENT"}' },
        { type: 'function_call_output', output: 'RESULT_ONLY' },
        { type: 'custom_tool_call', name: 'apply_patch', input: 'CUSTOM_INPUT' },
        { role: 'user', content: 'RESUMED_PROMPT' },
      ],
    } }
    expect(nativeModelConversationTurns(request)).toEqual([
      { role: 'user', text: 'ORIGINAL_PROMPT' },
      { role: 'assistant', text: 'ORIGINAL_ANSWER' },
      { role: 'assistant', text: 'CALL_ARGUMENT' },
      { role: 'assistant', text: 'CUSTOM_INPUT' },
      { role: 'user', text: 'RESUMED_PROMPT' },
    ])
    expect(nativeModelConversationTurns({ protocol: 'openai-responses', path: '/responses', body: { input: 'DIRECT_PROMPT' } })).toEqual([{ role: 'user', text: 'DIRECT_PROMPT' }])
  })

  it('reads Chat Completions assistant text and tool-call arguments without system or tool rows', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/chat/completions', body: {
      messages: [
        { role: 'system', content: 'SYSTEM_ONLY' },
        { role: 'user', content: [{ type: 'text', text: 'ORIGINAL_PROMPT' }, { type: 'image_url', image_url: { url: 'IMAGE_ONLY' } }] },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call', type: 'function', function: { name: 'answer', arguments: '{"answer":"ORIGINAL_ANSWER"}' } }] },
        { role: 'tool', tool_call_id: 'call', content: 'RESULT_ONLY' },
        { role: 'assistant', content: 'FOLLOW_UP', tool_calls: [{ function: { name: 'raw', arguments: 'RAW ARGUMENTS' } }, 'not a call'] },
        { role: 'user', content: 'RESUMED_PROMPT' },
      ],
    } }
    expect(nativeModelConversationTurns(request)).toEqual([
      { role: 'user', text: 'ORIGINAL_PROMPT' },
      { role: 'assistant', text: 'ORIGINAL_ANSWER' },
      { role: 'assistant', text: 'FOLLOW_UP\nRAW ARGUMENTS' },
      { role: 'user', text: 'RESUMED_PROMPT' },
    ])
  })

  it('reads Anthropic text and tool-use input without thinking or tool results', () => {
    const request: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', body: {
      system: [{ type: 'text', text: 'SYSTEM_ONLY' }],
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'ORIGINAL_PROMPT' }, { type: 'text', text: '실제 내용 🧪' }] },
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'THINKING_ONLY' }, { type: 'text', text: 'ORIGINAL_ANSWER' }, { type: 'tool_use', id: 'use', name: 'answer', input: { text: 'TOOL_INPUT' } }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'use', content: 'RESULT_ONLY' }, { type: 'text', text: 'RESUMED_PROMPT' }] },
      ],
    } }
    expect(nativeModelConversationTurns(request)).toEqual([
      { role: 'user', text: 'ORIGINAL_PROMPT\n실제 내용 🧪' },
      { role: 'assistant', text: 'ORIGINAL_ANSWER\nTOOL_INPUT' },
      { role: 'user', text: 'RESUMED_PROMPT' },
    ])
  })

  it('returns no turn for an empty message array', () => {
    expect(nativeModelConversationTurns({ protocol: 'anthropic-messages', path: '/v1/messages', body: { messages: [] } })).toEqual([])
  })

  it.each([
    { protocol: 'google-generative-language', body: { contents: null }, message: 'no contents array' },
    { protocol: 'openai-responses', body: {}, message: 'no input array' },
    { protocol: 'openai-chat-completions', body: { messages: 'broken' }, message: 'no message array' },
    { protocol: 'anthropic-messages', body: null, message: 'must be an object' },
    { protocol: 'openai-chat-completions', body: [], message: 'must be an object' },
  ] as const)('rejects a $protocol body without turns: $body', ({ protocol, body, message }) => {
    expect(() => nativeModelConversationTurns({ protocol, path: '/model', body })).toThrow(message)
  })

  it('requires a provider-owned service turn reader', () => {
    expect(() => nativeModelConversationTurns({ protocol: 'aws-event-stream', path: '/', body: { conversationState: {} } })).toThrow('own conversation turn reader')
  })
})

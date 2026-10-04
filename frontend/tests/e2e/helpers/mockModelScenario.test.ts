import type { MockModelServer } from './mockModelServer'
import { Buffer } from 'node:buffer'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import {
  HOUSEKEEPING_RULES,
  MOCK_SESSION_TITLE,
  mockScenarioPrompt,
  readScenarioStatus,
  registerAmbientScenario,
  registerMockModelScenario,
  removeMockModelScenario,
  withMockModelScenario,
} from './mockModelScenario'
import { AMBIENT_SCENARIO_ID, SCENARIO_MARKER } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'

const servers: MockModelServer[] = []

afterEach(async () => {
  vi.restoreAllMocks()
  await Promise.all(servers.splice(0).map(server => server.close()))
})

async function startServer(): Promise<MockModelServer> {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  servers.push(server)
  return server
}

function complete(server: MockModelServer, messages: Array<Record<string, unknown>>): Promise<string> {
  return fetch(`${server.url}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ stream: false, messages }),
  }).then(async (response) => {
    expect(response.status).toBe(200)
    const body = await response.json() as { choices: Array<{ message: { content: string } }> }
    return body.choices[0]!.message.content
  })
}

async function completeAnthropic(server: MockModelServer, system: string, text: string): Promise<string> {
  const response = await fetch(`${server.url}/v1/messages`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'claude-sonnet-5', max_tokens: 256, system: [{ type: 'text', text: system }], messages: [{ role: 'user', content: [{ type: 'text', text }] }] }),
  })
  expect(response.status).toBe(200)
  const body = await response.json() as { content: Array<{ type: string, text?: string }> }
  return body.content.filter(block => block.type === 'text').map(block => block.text ?? '').join('')
}

describe('mockScenarioPrompt', () => {
  it('puts the marker on the last line, so a first-line title stays the prompt', () => {
    const prompt = mockScenarioPrompt('scenario-1', 'Review the parser.\nThen report.')
    expect(prompt.split('\n')[0]).toBe('Review the parser.')
    expect(prompt.endsWith(`${SCENARIO_MARKER}scenario-1`)).toBe(true)
  })

  it('refuses an identifier the server would reject', () => {
    expect(() => mockScenarioPrompt('bad id', 'Run.')).toThrow('1 to 128 ASCII letters')
  })
})

describe('HOUSEKEEPING_RULES', () => {
  it('answers a plain title prompt without consuming a step', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'Primary answer' }], async (scenario) => {
      const marked = scenario.prompt('Inspect the parser.')
      const title = await complete(server, [
        { role: 'system', content: 'Generate a short title (four words or less) for this conversation.' },
        { role: 'user', content: marked },
      ])
      expect(title).toBe(MOCK_SESSION_TITLE)
      expect(await complete(server, [{ role: 'user', content: marked }])).toBe('Primary answer')
      expect(await scenario.status()).toMatchObject({ nextStep: 1, ruleMatches: { 'title-system': 1 } })
    })
  })

  it('answers a JSON title prompt with a JSON object', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'Primary answer' }], async (scenario) => {
      const marked = scenario.prompt('Inspect the parser.')
      const title = await complete(server, [
        { role: 'system', content: 'Generate a concise title.\nReturn exactly one valid JSON object: {"title":"..."}' },
        { role: 'user', content: marked },
      ])
      expect(JSON.parse(title)).toEqual({ title: MOCK_SESSION_TITLE })
      await complete(server, [{ role: 'user', content: marked }])
    })
  })

  it('answers a title prompt a provider sends as a user turn', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'Primary answer' }], async (scenario) => {
      const title = await complete(server, [
        { role: 'user', content: `Generate a title for this session.\n\n${SCENARIO_MARKER}${scenario.id}` },
      ])
      expect(title).toBe(MOCK_SESSION_TITLE)
      await complete(server, [{ role: 'user', content: scenario.prompt('Run.') }])
    })
  })

  // Grok forces its `session_title` tool for the first title of a session, and it
  // offers no switch for that call.
  it('answers Grok\'s forced title tool with a call of that tool', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'Primary answer' }], async (scenario) => {
      const marked = scenario.prompt('Inspect the parser.')
      const response = await fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          stream: false,
          tool_choice: { type: 'function', function: { name: 'session_title' } },
          messages: [
            { role: 'system', content: 'You are tasked with generating the session title. The user is asking software engineering questions.' },
            { role: 'user', content: `<user_query>\n${marked}\n</user_query>` },
          ],
        }),
      })
      expect(response.status).toBe(200)
      const body = await response.json() as { choices: Array<{ message: { tool_calls: Array<{ function: { name: string, arguments: string } }> } }> }
      const call = body.choices[0]!.message.tool_calls[0]!
      expect(call.function.name).toBe('session_title')
      expect(JSON.parse(call.function.arguments)).toEqual({ session_title: MOCK_SESSION_TITLE })
      expect(await complete(server, [{ role: 'user', content: marked }])).toBe('Primary answer')
      expect(await scenario.status()).toMatchObject({ nextStep: 1, ruleMatches: { 'title-grok': 1 } })
    })
  })

  // Kiro classifies the intent of a prompt before a turn in a spec mode, with the
  // prompt and its marker in the request.
  it('answers Kiro\'s intent classification without consuming a step', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'Primary answer' }], async (scenario) => {
      const kiroTurn = (agentMode: string) => fetch(`${server.url}/`, {
        method: 'POST',
        headers: { 'content-type': 'application/x-amz-json-1.0', 'x-amz-target': 'KiroRuntimeService.GenerateAssistantResponse' },
        body: JSON.stringify({
          conversationState: { conversationId: 's', history: [], currentMessage: { userInputMessage: { content: scenario.prompt('Build a to-do app.') } } },
          agentMode,
        }),
      })
      const classification = await kiroTurn('intent-classification')
      expect(classification.status).toBe(200)
      expect(Buffer.from(await classification.arrayBuffer()).toString('utf8')).toContain('specGeneration')
      const turn = await kiroTurn('spec')
      expect(turn.status).toBe(200)
      expect(Buffer.from(await turn.arrayBuffer()).toString('utf8')).toContain('Primary answer')
      expect(await scenario.status()).toMatchObject({ nextStep: 1, ruleMatches: { 'intent-kiro': 1 } })
    })
  })

  it('leaves a coding system prompt that mentions Title Case to the queue', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'Primary answer' }], async (scenario) => {
      const answer = await complete(server, [
        { role: 'system', content: 'You are a coding agent. Keep headers short and write them in **Title Case**.' },
        { role: 'user', content: scenario.prompt('Review the parser.') },
      ])
      expect(answer).toBe('Primary answer')
    })
  })

  it('lets a test rule win over the housekeeping rule for the same turn', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, {
      steps: [{ text: 'Primary answer' }],
      rules: [{ name: 'own-title', when: { system: 'generate a short title' }, respond: { text: 'Chosen title' } }],
    }, async (scenario) => {
      const marked = scenario.prompt('Inspect the parser.')
      const title = await complete(server, [
        { role: 'system', content: 'Generate a short title for this conversation.' },
        { role: 'user', content: marked },
      ])
      expect(title).toBe('Chosen title')
      await complete(server, [{ role: 'user', content: marked }])
    })
  })

  it('makes every request consume a step when a test drops the defaults', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, {
      steps: [{ text: 'Only step' }],
      housekeeping: [],
    }, async (scenario) => {
      const title = await complete(server, [
        { role: 'system', content: 'Generate a short title for this conversation.' },
        { role: 'user', content: scenario.prompt('Inspect the parser.') },
      ])
      expect(title).toBe('Only step')
    })
  })

  it('states one name for each rule, so a status counts them unambiguously', () => {
    expect(new Set(HOUSEKEEPING_RULES.map(rule => rule.name)).size).toBe(HOUSEKEEPING_RULES.length)
  })
})

describe('withMockModelScenario', () => {
  it('runs and removes a complete scenario', async () => {
    const server = await startServer()
    let id = ''
    const result = await withMockModelScenario(server.url, [{ text: 'Client response' }], async (scenario) => {
      id = scenario.id
      expect(await complete(server, [{ role: 'user', content: scenario.prompt('Run.') }])).toBe('Client response')
      return 'done'
    })

    expect(result).toBe('done')
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}`)).status).toBe(404)
  })

  it('derives a script from its generated scenario identifier', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, scenario => [{ text: scenario.id }], async (scenario) => {
      expect(await complete(server, [{ role: 'user', content: scenario.prompt('Run.') }])).toBe(scenario.id)
    })
  })

  it('reports an unconsumed script with its status and removes the scenario', async () => {
    const server = await startServer()
    let id = ''
    await expect(withMockModelScenario(server.url, [{ text: 'A' }, { text: 'B' }], async (scenario) => {
      id = scenario.id
      await complete(server, [{ role: 'user', content: scenario.prompt('Run.') }])
    })).rejects.toThrow('1 of 2 steps consumed, 0 unexpected requests')
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}`)).status).toBe(404)
  })

  it('reports an unexpected request even when every step was consumed', async () => {
    const server = await startServer()
    await expect(withMockModelScenario(server.url, [{ text: 'A' }], async (scenario) => {
      const marked = scenario.prompt('Run.')
      await complete(server, [{ role: 'user', content: marked }])
      expect((await fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ messages: [{ role: 'user', content: marked }] }),
      })).status).toBe(409)
    })).rejects.toThrow('1 unexpected request')
  })

  it('preserves a callback failure and removes its scenario', async () => {
    const server = await startServer()
    let id = ''
    const failure = new Error('scenario callback failed')
    const result = withMockModelScenario(server.url, [{ text: 'Unused response' }], async (scenario) => {
      id = scenario.id
      throw failure
    }).catch(error => error)

    expect(await result).toBe(failure)
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}`)).status).toBe(404)
  })
})

describe('registerAmbientScenario', () => {
  it('answers a housekeeping turn that carries no marker and records anything else', async () => {
    const server = await startServer()
    await registerAmbientScenario(server.url)

    const title = await complete(server, [
      { role: 'system', content: 'Generate a short title for this conversation.' },
      { role: 'user', content: 'Some earlier work.' },
    ])
    expect(title).toBe(MOCK_SESSION_TITLE)

    const content = await fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ messages: [{ role: 'user', content: 'An unscripted turn.' }] }),
    })
    expect(content.status).toBe(409)
    const status = await readScenarioStatus(server.url, AMBIENT_SCENARIO_ID)
    expect(status.unexpectedRequests).toMatchObject([{ reason: 'The scenario has no remaining scripted answer.' }])
  })
})

describe('removeMockModelScenario', () => {
  it('serializes the unused-queue policy for an actual server deletion', async () => {
    const server = await startServer()
    await registerMockModelScenario(server.url, 'client-unused-policy', { steps: [{ text: 'Unused.' }] })
    const nativeFetch = globalThis.fetch.bind(globalThis)
    const queries: Array<string | null> = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, options) => {
      const target = typeof input === 'string' ? input : 'url' in input ? input.url : input.toString()
      const url = new URL(target)
      if (options?.method === 'DELETE')
        queries.push(url.searchParams.get('allow-unconsumed'))
      return nativeFetch(input, options)
    })
    const removed = await removeMockModelScenario(server.url, 'client-unused-policy', { allowUnconsumed: true })
    expect(queries).toEqual(['true'])
    expect(removed).toBeUndefined()
    await expect(readScenarioStatus(server.url, 'client-unused-policy')).rejects.toThrow('Could not read model scenario')
  })

  it('returns the actual unexpected-request status under the unused-queue policy', async () => {
    const server = await startServer()
    const id = 'client-refused-policy'
    await registerMockModelScenario(server.url, id, { steps: [] })
    const unexpected = await fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: mockScenarioPrompt(id, 'This turn is unexpected.') }] }),
    })
    expect(unexpected.status).toBe(409)
    await unexpected.arrayBuffer()
    const refused = await removeMockModelScenario(server.url, id, { allowUnconsumed: true })
    expect(refused).toMatchObject({ complete: false, unexpectedRequests: [{ reason: 'The scenario has no remaining scripted answer.' }] })
    expect((await readScenarioStatus(server.url, id)).unexpectedRequests).toHaveLength(1)
    expect(await removeMockModelScenario(server.url, id, { force: true })).toBeUndefined()
  })

  it('rejects contradictory removal options before HTTP access', async () => {
    const server = await startServer()
    await registerMockModelScenario(server.url, 'client-contradictory-policy', { steps: [{ text: 'Unused.' }] })
    const nativeFetch = globalThis.fetch.bind(globalThis)
    const requests = vi.spyOn(globalThis, 'fetch').mockImplementation(nativeFetch)
    await expect(Reflect.apply(removeMockModelScenario, undefined, [server.url, 'client-contradictory-policy', { force: true, allowUnconsumed: true }])).rejects.toThrow('Scenario deletion cannot combine force and allow-unconsumed.')
    expect(requests).not.toHaveBeenCalled()
  })

  it('returns the status when the script is unconsumed and removes it under force', async () => {
    const server = await startServer()
    await registerMockModelScenario(server.url, 'manual', { steps: [{ text: 'A' }] })

    const refused = await removeMockModelScenario(server.url, 'manual')
    expect(refused).toMatchObject({ complete: false, nextStep: 0, stepCount: 1 })
    expect(await removeMockModelScenario(server.url, 'manual', { force: true })).toBeUndefined()
    await expect(readScenarioStatus(server.url, 'manual')).rejects.toThrow('Could not read model scenario')
  })

  it('reports a registration the server refused', async () => {
    const server = await startServer()
    await registerMockModelScenario(server.url, 'taken', { steps: [{ text: 'A' }] })
    await expect(registerMockModelScenario(server.url, 'taken', { steps: [{ text: 'B' }] }))
      .rejects
      .toThrow('Could not register model scenario taken: 409')
  })
})

describe('native Claude Auto preflight', () => {
  it('keeps the native harm classifier separate from the actual child and root turns', async () => {
    const server = await startServer()
    const id = 'classifier-rule'
    const childTask = mockScenarioPrompt(id, 'Count slowly to one hundred.')
    await registerMockModelScenario(server.url, id, {
      steps: [{ text: 'The actual root answer.' }],
      rules: [{ name: 'actual-child', when: { user: 'Count slowly to one hundred' }, respond: { text: 'The actual child answer.' } }],
    })
    const classifier = await completeAnthropic(server, 'You are a security monitor for autonomous AI coding agents.', `<transcript>\n${childTask}\n</transcript>\nRespond with <severity>N</severity> ONLY. Grade HARM ONLY.`)
    expect(classifier).toBe('<severity>0</severity>')
    const preflight = await readScenarioStatus(server.url, id)
    expect(preflight.nextStep).toBe(0)
    expect(preflight.ruleMatches['actual-child'] ?? 0).toBe(0)
    expect(await completeAnthropic(server, 'You are a Claude agent.', childTask)).toBe('The actual child answer.')
    expect(await completeAnthropic(server, 'You are a Claude agent.', mockScenarioPrompt(id, 'Finish the original root task.'))).toBe('The actual root answer.')
    const completed = await readScenarioStatus(server.url, id)
    expect(completed.nextStep).toBe(1)
    expect(completed.ruleMatches['actual-child']).toBe(1)
    expect(completed.ruleMatches['claude-auto-harm']).toBe(1)
    expect(completed.unexpectedRequests).toEqual([])
  })
})

describe('native rule priority selection', () => {
  it('leaves a different model protocol in the content queue despite both classifier signatures', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'The actual content turn.' }], async (scenario) => {
      expect(await complete(server, [
        { role: 'system', content: 'You are a security monitor for autonomous AI coding agents.' },
        { role: 'user', content: scenario.prompt('Respond with <severity>N</severity> ONLY.') },
      ])).toBe('The actual content turn.')
      expect((await scenario.status()).ruleMatches['claude-auto-harm'] ?? 0).toBe(0)
    })
  })
  it('keeps declaration order and once guards within each priority', async () => {
    const server = await startServer()
    await withMockModelScenario(server.url, {
      steps: [{ text: 'The original ordered turn.' }],
      rules: [
        { name: 'normal-first', priority: 'normal', when: { user: 'Select a rule' }, respond: { text: 'Normal first.' } },
        { name: 'high-first', priority: 'high', once: true, when: { user: 'Select a rule' }, respond: { text: 'High first.' } },
        { name: 'high-second', priority: 'high', once: true, when: { user: 'Select a rule' }, respond: { text: 'High second.' } },
        { name: 'normal-second', when: { user: 'Select a rule' }, respond: { text: 'Normal second.' } },
      ],
    }, async (scenario) => {
      const request = [{ role: 'user', content: scenario.prompt('Select a rule.') }]
      expect(await complete(server, request)).toBe('High first.')
      expect(await complete(server, request)).toBe('High second.')
      expect(await complete(server, request)).toBe('Normal first.')
      expect((await scenario.status()).nextStep).toBe(0)
      expect(await complete(server, [{ role: 'user', content: scenario.prompt('Consume the actual content turn.') }])).toBe('The original ordered turn.')
      expect((await scenario.status()).ruleMatches).toEqual({ 'high-first': 1, 'high-second': 1, 'normal-first': 1 })
    })
  })

  it.each([
    { system: 'You are a coding agent.', user: 'Respond with <severity>N</severity> ONLY.' },
    { system: 'You are a security monitor for autonomous AI coding agents.', user: 'Answer this normal coding prompt.' },
  ])('leaves a classifier lookalike in the content queue: $system', async ({ system, user }) => {
    const server = await startServer()
    await withMockModelScenario(server.url, [{ text: 'The actual content turn.' }], async (scenario) => {
      expect(await completeAnthropic(server, system, scenario.prompt(user))).toBe('The actual content turn.')
      expect((await scenario.status()).ruleMatches['claude-auto-harm'] ?? 0).toBe(0)
    })
  })
})

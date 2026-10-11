import type { MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { MOCK_SESSION_TITLE, readScenarioStatus } from './mockModelScenario'
import { MAX_SCENARIO_REQUEST_RECORDS } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { expectTurnEndedAfter, modelScriptFixtures, startModelScript } from './modelScriptFixture'
import { currentTestDeadline, WAIT_REPORT_MARGIN_MS } from './testDeadline'

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

function complete(server: MockModelServer, messages: Array<Record<string, unknown>>): Promise<Response> {
  return fetch(`${server.url}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ stream: false, messages }),
  })
}

async function answer(server: MockModelServer, prompt: string): Promise<string> {
  const response = await complete(server, [{ role: 'user', content: prompt }])
  expect(response.status).toBe(200)
  const body = await response.json() as { choices: Array<{ message: { content: string } }> }
  return body.choices[0]!.message.content
}

describe('modelScriptFixtures', () => {
  type StartFixture = (args: object, use: (startedAt: number) => Promise<void>, testInfo: { timeout: number }) => Promise<void>

  function startFixture(): StartFixture {
    const registration = modelScriptFixtures.testStartedAt
    if (!Array.isArray(registration) || typeof registration[0] !== 'function')
      throw new Error('The testStartedAt fixture is not an automatic fixture with a callback.')
    expect(registration[1]).toEqual({ auto: true })
    return registration[0] as unknown as StartFixture
  }

  it('records the test deadline while the test runs, from the live test timeout', async () => {
    const testInfo = { timeout: 120_000 }
    const seen: Array<number | undefined> = []
    await startFixture()({}, async (startedAt) => {
      seen.push(currentTestDeadline(), startedAt + 120_000)
      testInfo.timeout = 240_000
      seen.push(currentTestDeadline(), startedAt + 240_000)
    }, testInfo)
    expect(seen[0]).toBe(seen[1])
    expect(seen[2]).toBe(seen[3])
    expect(currentTestDeadline()).toBeUndefined()
  })

  it('ends the record when the test fails', async () => {
    const failure = new Error('The test body failed.')
    await expect(startFixture()({}, async () => {
      throw failure
    }, { timeout: 120_000 })).rejects.toBe(failure)
    expect(currentTestDeadline()).toBeUndefined()
  })
})

describe('expectTurnEndedAfter', () => {
  function status(nextStep: number, unexpected = 0): MockModelScenarioStatus {
    return {
      complete: true,
      nextStep,
      stepCount: nextStep,
      requests: [],
      unexpectedRequests: Array.from({ length: unexpected }, () => ({ protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', reason: 'unscripted', body: {} })),
      ruleMatches: {},
      pendingGates: [],
    }
  }

  it('accepts a turn that requested exactly the stated steps', async () => {
    await expect(expectTurnEndedAfter({ status: async () => status(3) }, 3)).resolves.toBeUndefined()
  })

  it.each([2, 4])('refuses a turn that requested %i ordered steps instead of 3', async (nextStep) => {
    await expect(expectTurnEndedAfter({ status: async () => status(nextStep) }, 3)).rejects.toThrow('the agent requested 3 ordered steps and no more')
  })

  it('refuses a request that the script did not expect', async () => {
    await expect(expectTurnEndedAfter({ status: async () => status(3, 1) }, 3)).rejects.toThrow('the agent sent no request that the script did not expect')
  })

  it.each([0, -1, 1.5, Number.NaN])('refuses the step count %s before it reads the script', async (nextStep) => {
    const read = vi.fn(async () => status(1))
    await expect(expectTurnEndedAfter({ status: read }, nextStep)).rejects.toThrow('one or more ordered steps')
    expect(read).not.toHaveBeenCalled()
  })
})

describe('startModelScript', () => {
  // A cleanup that releases a gate runs after the native client may have cancelled the gated request.
  // The cancelled request leaves the gate with nothing to release, and that cleanup must not fail.
  it('reports no release, and does not fail, for a gated response that the client cancelled', async () => {
    const server = await createMockModelServer({ models: ['native-cleanup-unit'] })
    servers.push(server)
    const { script, finish } = await startModelScript(server.url)
    const controller = new AbortController()
    try {
      await script.queue({ text: 'The interrupted answer must not complete.', gate: 'native-cancelled-response' })
      const pending = fetch(`${server.url}/v1/messages`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-api-key': 'leapmux-e2e-model-key' },
        body: JSON.stringify({ model: 'native-cleanup-unit', max_tokens: 100, stream: true, messages: [{ role: 'user', content: script.prompt('Hold the native cleanup test response.') }] }),
        signal: controller.signal,
      }).then(() => null, error => error)
      await script.waitForGate('native-cancelled-response')
      controller.abort()
      await pending
      await expect.poll(async () => (await script.status()).pendingGates).toEqual([])
      await expect(script.releaseGateIfHeld('native-cancelled-response')).resolves.toBe(false)
    }
    finally {
      controller.abort()
      await finish(false)
    }
  })

  it('rejects native requests that arrive immediately before an allowed queue removal', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'The first native turn has a scripted answer.' })
    script.allowUnconsumed('the test interrupts before every queued answer is requested')
    const nativeFetch = globalThis.fetch.bind(globalThis)
    let injected = false
    const nativeStatuses: number[] = []
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, options) => {
      const target = typeof input === 'string' ? input : 'url' in input ? input.url : input.toString()
      const url = new URL(target)
      if (!injected && options?.method === 'DELETE' && url.pathname.endsWith(`/${script.id}`)) {
        injected = true
        for (const text of ['The scripted turn arrives.', 'The extra native turn has no answer.']) {
          const response = await complete(server, [{ role: 'user', content: script.prompt(text) }])
          nativeStatuses.push(response.status)
          await response.arrayBuffer()
        }
        await script.queue({ text: 'The interrupted turn never requests this later answer.' })
      }
      return nativeFetch(input, options)
    })
    await expect(finish(true)).rejects.toThrow('1 request the script did not answer')
    expect(injected).toBe(true)
    expect(nativeStatuses).toEqual([200, 409])
    await expect(readScenarioStatus(server.url, script.id)).rejects.toThrow('Could not read model scenario')
  })

  it('force-removes an unexpected request after an already failed test', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const unexpected = await complete(server, [{ role: 'user', content: script.prompt('The already failed test sends an unexpected turn.') }])
    expect(unexpected.status).toBe(409)
    await unexpected.arrayBuffer()
    await expect(finish(false)).resolves.toBeUndefined()
    await expect(readScenarioStatus(server.url, script.id)).rejects.toThrow('Could not read model scenario')
  })

  it('keeps the strict-script failure when forced cleanup also fails', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const unexpected = await complete(server, [{ role: 'user', content: script.prompt('This native request is unscripted.') }])
    expect(unexpected.status).toBe(409)
    await unexpected.arrayBuffer()
    await script.queue({ text: 'This later answer stays unused.' })
    script.allowUnconsumed('the native turn was interrupted')
    const nativeFetch = globalThis.fetch.bind(globalThis)
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, options) => {
      const target = typeof input === 'string' ? input : 'url' in input ? input.url : input.toString()
      const url = new URL(target)
      if (options?.method === 'DELETE' && url.searchParams.get('force') === 'true')
        return new Response('The cleanup transport failed.', { status: 503 })
      return nativeFetch(input, options)
    })
    await expect(finish(true)).rejects.toMatchObject({
      errors: [
        expect.objectContaining({ message: expect.stringMatching(/incomplete:.*1 request the script did not answer/) }),
        expect.objectContaining({ message: expect.stringContaining('503 The cleanup transport failed.') }),
      ],
    })
  })

  it('fails teardown for an unscripted request even when an unconsumed queue is permitted', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const unexpected = await complete(server, [{ role: 'user', content: script.prompt('The native turn was not scripted.') }])
    expect(unexpected.status).toBe(409)
    await script.queue({ text: 'The interrupted provider never requests this answer.' })
    script.allowUnconsumed('the test interrupts the native turn before the next model request')
    await expect(finish(true)).rejects.toThrow('1 request the script did not answer')
    await expect(readScenarioStatus(server.url, script.id)).rejects.toThrow('Could not read model scenario')
  })

  it('rejects a whitespace-only queue exception reason', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    expect(() => script.allowUnconsumed(' \n\t')).toThrow('needs the reason')
    await finish(true)
  })

  it('reads the current whole-test deadline without storing an old value', async () => {
    const server = await startServer()
    let deadline: number | undefined
    const { script, finish } = await startModelScript(server.url, { testDeadline: () => deadline })
    expect(script.testDeadline()).toBeUndefined()
    deadline = 240_000
    expect(script.testDeadline()).toBe(240_000)
    deadline = 360_000
    expect(script.testDeadline()).toBe(360_000)
    deadline = undefined
    expect(script.testDeadline()).toBeUndefined()
    await finish(true)
  })

  it('returns no whole-test deadline when its fixture declares none', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    expect(script.testDeadline()).toBeUndefined()
    await finish(true)
  })

  it('registers a scenario that answers a housekeeping turn before any step exists', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)

    const title = await complete(server, [
      { role: 'system', content: 'Generate a short title for this conversation.' },
      { role: 'user', content: script.prompt('Run.') },
    ])
    expect(title.status).toBe(200)
    await finish(true)
  })

  it('answers queued steps in order and verifies consumption at teardown', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'First' }, { text: 'Second' })

    const prompt = script.prompt('Run.')
    expect(await answer(server, prompt)).toBe('First')
    expect(await answer(server, prompt)).toBe('Second')
    expect(await script.status()).toMatchObject({ complete: true, nextStep: 2, stepCount: 2 })
    await finish(true)
  })

  it('returns the step index of the first answer that each queue call appends', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    expect(await script.queue({ text: 'First' }, { text: 'Second' })).toBe(0)
    expect(await script.queue({ text: 'Third' })).toBe(2)
    expect(await script.queue({ text: 'Fourth' })).toBe(3)
    script.allowUnconsumed('the test reads only the indexes that the queue returns')
    await finish(true)
  })

  it('appends concurrent queue calls in call order', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const [first, second, third] = await Promise.all([
      script.queue({ text: 'A1' }, { text: 'A2' }),
      script.queue({ text: 'B1' }),
      script.queue({ text: 'C1' }, { text: 'C2' }, { text: 'C3' }),
    ])
    expect([first, second, third]).toEqual([0, 2, 3])
    const prompt = script.prompt('Run.')
    const answers: string[] = []
    for (let index = 0; index < 6; index++)
      answers.push(await answer(server, prompt))
    expect(answers).toEqual(['A1', 'A2', 'B1', 'C1', 'C2', 'C3'])
    await finish(true)
  })

  it('keeps the count after a refused append, so the next call returns the true index', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    expect(await script.queue({ text: 'First' })).toBe(0)
    // A negative delay fails the server's step validation, so the server appends nothing.
    await expect(script.queue({ text: 'Refused', delayMs: -1 })).rejects.toThrow('Could not extend model scenario')
    expect(await script.queue({ text: 'Second' })).toBe(1)
    const prompt = script.prompt('Run.')
    expect(await answer(server, prompt)).toBe('First')
    expect(await answer(server, prompt)).toBe('Second')
    await finish(true)
  })

  it('returns the request record of a step after the agent requests it', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const start = await script.queue({ text: 'First' }, { text: 'Second' })
    const first = script.prompt('The first native turn.')
    const second = script.prompt('The second native turn.')
    const pending = script.requestAt(start + 1)
    expect(await answer(server, first)).toBe('First')
    expect(await answer(server, second)).toBe('Second')
    const request = await pending
    expect(request.stepIndex).toBe(start + 1)
    expect(JSON.stringify(request.body)).toContain('The second native turn.')
    expect(JSON.stringify((await script.requestAt(start)).body)).toContain('The first native turn.')
    await finish(true)
  })

  it('fails with the script state when the agent never requests the step', async () => {
    const server = await startServer()
    let deadline: number | undefined
    const { script, finish } = await startModelScript(server.url, { testDeadline: () => deadline })
    await script.queue({ text: 'Never asked for' })
    deadline = Date.now() + WAIT_REPORT_MARGIN_MS + 300
    await expect(script.requestAt(0)).rejects.toThrow(/reached 0 of 1 answers in \d+ms, before the test's own timeout: 0 of 1 queued answers consumed/)
    await finish(false)
  })

  it('fails with the record cap when the server dropped the record of a consumed step', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const steps = Array.from({ length: MAX_SCENARIO_REQUEST_RECORDS + 1 }, (_, index) => ({ text: `Answer ${index}` }))
    const start = await script.queue(...steps)
    const prompt = script.prompt('Run.')
    for (const step of steps)
      expect(await answer(server, prompt)).toBe(step.text)
    await expect(script.requestAt(start)).rejects.toThrow(
      `The model script holds no request for step ${start}: the agent requested it, but the server keeps only the newest ${MAX_SCENARIO_REQUEST_RECORDS} request records`,
    )
    expect((await script.requestAt(start + 1)).stepIndex).toBe(start + 1)
    await finish(true)
  })

  it.each([-1, 0.5, Number.NaN])('refuses the step index %s before any wait', async (index) => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await expect(script.requestAt(index)).rejects.toThrow('A model script step index must be a nonnegative safe integer')
    await finish(true)
  })

  it('appends a later queue call to the end of the same script', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'First' })
    const prompt = script.prompt('Run.')
    expect(await answer(server, prompt)).toBe('First')

    await script.queue({ text: 'Second' })
    expect(await answer(server, prompt)).toBe('Second')
    await finish(true)
  })

  it('answers through a rule without consuming a queued step', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'Queued' })
    await script.rule({ name: 'retry', when: { user: 'retry this' }, respond: { text: 'Retried' } })

    expect(await answer(server, script.prompt('Please retry this.'))).toBe('Retried')
    expect(await answer(server, script.prompt('Please retry this.'))).toBe('Retried')
    expect(await answer(server, script.prompt('Run.'))).toBe('Queued')
    await finish(true)
  })

  // The housekeeping rules have high priority, so only a high-priority rule can replace one.
  it('lets a high-priority rule the test adds win over the housekeeping rule it replaces', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.rule({ name: 'own-title', priority: 'high', when: { system: 'generate a short title' }, respond: { text: 'Chosen' } })

    const title = await complete(server, [
      { role: 'system', content: 'Generate a short title for this conversation.' },
      { role: 'user', content: script.prompt('Run.') },
    ])
    expect((await title.json() as { choices: Array<{ message: { content: string } }> }).choices[0]!.message.content)
      .toBe('Chosen')
    await finish(true)
  })

  it('still answers a housekeeping turn the test did not replace', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    const title = await complete(server, [
      { role: 'system', content: 'Generate a short title for this conversation.' },
      { role: 'user', content: script.prompt('Run.') },
    ])
    expect((await title.json() as { choices: Array<{ message: { content: string } }> }).choices[0]!.message.content)
      .toBe(MOCK_SESSION_TITLE)
    await finish(true)
  })

  it('fails the teardown for an unconsumed queue and still removes the scenario', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'Never asked for' })

    await expect(finish(true)).rejects.toThrow('0 of 1 queued answers consumed')
    await expect(readScenarioStatus(server.url, script.id)).rejects.toThrow('Could not read model scenario')
  })

  it('fails the teardown for a request the script did not answer', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    expect((await complete(server, [{ role: 'user', content: script.prompt('Unscripted.') }])).status).toBe(409)

    await expect(finish(true)).rejects.toThrow('1 request the script did not answer')
  })

  it('skips the check after a failure, so the first cause survives', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'Never asked for' })

    await expect(finish(false)).resolves.toBeUndefined()
    await expect(readScenarioStatus(server.url, script.id)).rejects.toThrow('Could not read model scenario')
  })

  it('accepts an unconsumed queue for a stated reason', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'Interrupted before the agent asked' })
    script.allowUnconsumed('the test cancels the turn')

    await expect(finish(true)).resolves.toBeUndefined()
  })

  it('refuses an empty reason, so every exception is documented', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    expect(() => script.allowUnconsumed('')).toThrow('needs the reason')
    await finish(true)
  })

  it('waits until the agent consumed the answers it queued', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'First' }, { text: 'Second' })
    const prompt = script.prompt('Run.')

    // The requests race the wait, which is what a real turn does.
    const turns = (async () => {
      await answer(server, prompt)
      await answer(server, prompt)
    })()
    expect(await script.waitForSteps(2)).toMatchObject({ nextStep: 2, complete: true })
    await turns
    await finish(true)
  })

  it('waits for a held model request and releases its answer', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'Child answer.', gate: 'child-answer' })

    const answerPromise = answer(server, script.prompt('Ask the child.'))
    const status = await script.waitForGate('child-answer')
    expect(status).toMatchObject({ nextStep: 1, pendingGates: ['child-answer'], complete: false })
    await script.releaseGate('child-answer')
    expect(await answerPromise).toBe('Child answer.')
    await finish(true)
  })

  it('reports the state it reached when the answers never arrive', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'Never asked for' })

    await expect(script.waitForSteps(1, 120)).rejects.toThrow('reached 0 of 1 answers in 120ms')
    script.allowUnconsumed('the wait proved the agent asked for nothing')
    await finish(true)
  })

  it('waits for every answer queued so far when given no count', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'First' })
    await answer(server, script.prompt('Run.'))
    expect(await script.waitForSteps()).toMatchObject({ nextStep: 1 })

    await script.queue({ text: 'Second' })
    const second = answer(server, script.prompt('Run.'))
    expect(await script.waitForSteps()).toMatchObject({ nextStep: 2 })
    await second
    await finish(true)
  })

  it('returns at once when the answers were already consumed', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.queue({ text: 'First' })
    await answer(server, script.prompt('Run.'))

    expect(await script.waitForSteps(1, 0)).toMatchObject({ nextStep: 1 })
    await finish(true)
  })

  it('gives each test its own script', async () => {
    const server = await startServer()
    const first = await startModelScript(server.url)
    const second = await startModelScript(server.url)
    expect(first.script.id).not.toBe(second.script.id)
    await first.script.queue({ text: 'First script' })
    await second.script.queue({ text: 'Second script' })

    expect(await answer(server, second.script.prompt('Run.'))).toBe('Second script')
    expect(await answer(server, first.script.prompt('Run.'))).toBe('First script')
    await first.finish(true)
    await second.finish(true)
  })

  // A stalled wait must report script progress before the whole-test timeout.
  // Set the deadline after server setup so that setup cannot consume the test interval.
  // Leave 300ms for the wait plus the existing report margin.
  // Require the wait to fail before the test deadline without fixing its exact poll time.
  it('ends a stalled wait before the test deadline, with the progress of the script', async () => {
    const server = await startServer()
    let deadline: number | undefined
    const { script, finish } = await startModelScript(server.url, { testDeadline: () => deadline })
    await script.queue({ text: 'Never asked for' })

    deadline = Date.now() + WAIT_REPORT_MARGIN_MS + 300
    await expect(script.waitForSteps()).rejects.toThrow(/reached 0 of 1 answers in \d+ms, before the test's own timeout/)
    expect(Date.now()).toBeLessThan(deadline)
    await finish(false)
  })

  it('ends at once when the test deadline leaves no time for the wait', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url, { testDeadline: () => Date.now() })
    await script.queue({ text: 'Never asked for' })
    await expect(script.waitForSteps()).rejects.toThrow(/reached 0 of 1 answers/)
    await finish(false)
  })

  // A test without a whole-test deadline keeps the caller's wait limit.
  it('keeps the limit that the caller states when the test has no deadline', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url, { testDeadline: () => undefined })
    await script.queue({ text: 'Never asked for' })
    await expect(script.waitForSteps(1, 200)).rejects.toThrow(/reached 0 of 1 answers/)
    await finish(false)
  })

  // The conversation of a long goal loop grows with each turn, so one fallback request can hold
  // megabytes. A message with the whole status once reached 169 MB. Playwright copies a failure
  // message into several reports, and the copies exhausted the 4 GB heap of the shard process.
  it('keeps the request bodies out of the incomplete-script message', async () => {
    const server = await startServer()
    const attached: MockModelScenarioStatus[] = []
    const { script, finish } = await startModelScript(server.url, {
      attachStatus: async (status) => {
        attached.push(status)
      },
    })
    await script.queue({ text: 'The scripted answer.' })
    expect(await answer(server, script.prompt('One scripted turn.'))).toBe('The scripted answer.')
    const marker = `BODY_MARKER_${'x'.repeat(200_000)}`
    for (const index of [1, 2, 3]) {
      const unexpected = await complete(server, [{ role: 'user', content: script.prompt(`Unscripted request ${index} ${marker}`) }])
      expect(unexpected.status).toBe(409)
      await unexpected.arrayBuffer()
    }

    const failure = await finish(true).then(() => undefined, (error: unknown) => error as Error)

    expect(failure?.message).toMatch(/incomplete:.*3 requests the script did not answer/)
    expect(failure?.message).not.toContain('BODY_MARKER_')
    expect(failure?.message.length).toBeLessThan(20_000)
    expect(failure?.message).toContain('/v1/chat/completions')
    expect(attached).toHaveLength(1)
    expect(JSON.stringify(attached[0])).toContain('BODY_MARKER_')
  })

  it('attaches nothing when the script is complete', async () => {
    const server = await startServer()
    const attached: MockModelScenarioStatus[] = []
    const { script, finish } = await startModelScript(server.url, {
      attachStatus: async (status) => {
        attached.push(status)
      },
    })
    await script.queue({ text: 'The scripted answer.' })
    expect(await answer(server, script.prompt('One scripted turn.'))).toBe('The scripted answer.')
    await finish(true)
    expect(attached).toEqual([])
  })

  it('keeps the failure when the attachment fails', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url, {
      attachStatus: async () => {
        throw new Error('The disk is full.')
      },
    })
    const unexpected = await complete(server, [{ role: 'user', content: script.prompt('An unscripted turn.') }])
    expect(unexpected.status).toBe(409)
    await unexpected.arrayBuffer()

    await expect(finish(true)).rejects.toThrow(/incomplete:.*1 request the script did not answer/)
  })
})

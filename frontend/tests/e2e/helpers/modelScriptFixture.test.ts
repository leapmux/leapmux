import type { MockModelServer } from './mockModelServer'
import { afterEach, describe, expect, it } from 'vitest'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { MOCK_SESSION_TITLE, readScenarioStatus } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'
import { startModelScript } from './modelScriptFixture'

const servers: MockModelServer[] = []

afterEach(async () => {
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

describe('startModelScript', () => {
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

  it('lets a rule the test adds win over the housekeeping rule it replaces', async () => {
    const server = await startServer()
    const { script, finish } = await startModelScript(server.url)
    await script.rule({ name: 'own-title', when: { system: 'generate a short title' }, respond: { text: 'Chosen' } })

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
})

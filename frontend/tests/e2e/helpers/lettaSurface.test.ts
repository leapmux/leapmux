import type { IncomingMessage, ServerResponse } from 'node:http'
import { Readable } from 'node:stream'
import { afterEach, describe, expect, it } from 'vitest'
import { LETTA_CHATGPT_ACCESS_TOKEN, LETTA_CHATGPT_ACCOUNT_ID } from './lettaEnvironment'
import { registerMockModelScenario, removeMockModelScenario } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'
import { createScenarioQuota } from './scenarioQuota'

const servers: Awaited<ReturnType<typeof createMockModelServer>>[] = []

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => server.close()))
})

describe('native Letta quota service', () => {
  it('validates the native account and retains response receipts across profile changes', async () => {
    const server = await createMockModelServer({ models: ['letta-quota-test'] })
    servers.push(server)
    await registerMockModelScenario(server.url, 'letta-quota-receipts', { steps: [] })
    const control = new URL('/__e2e/letta/quota/letta-quota-receipts', server.url)
    const profile = (status: number, body: unknown) => fetch(control, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ status, body, retryAfterSeconds: 2 }) })
    expect((await profile(429, { error: 'throttled' })).status).toBe(200)
    const usage = new URL('/backend-api/wham/usage', server.url)
    expect((await fetch(usage)).status).toBe(401)
    const headers = { 'authorization': `Bearer ${LETTA_CHATGPT_ACCESS_TOKEN}`, 'chatgpt-account-id': LETTA_CHATGPT_ACCOUNT_ID }
    expect((await fetch(usage, { headers: { ...headers, 'chatgpt-account-id': 'another' } })).status).toBe(401)
    const refused = await fetch(usage, { headers })
    expect(refused.status).toBe(429)
    expect(refused.headers.get('retry-after')).toBe('2')
    expect((await profile(200, { rate_limit: { primary_window: { used_percent: 0 } } })).status).toBe(200)
    expect(await (await fetch(usage, { headers })).json()).toEqual({ rate_limit: { primary_window: { used_percent: 0 } } })
    const state = await (await fetch(control)).json()
    expect(state.receipts.map((receipt: { status: number }) => receipt.status)).toEqual([401, 401, 429, 200])
    expect(state.totalRequests).toBe(4)
    expect(JSON.stringify(state)).not.toContain(LETTA_CHATGPT_ACCESS_TOKEN)
  })

  it('refuses invalid profiles and another active scenario without changing the owner', async () => {
    const server = await createMockModelServer({ models: ['letta-quota-test'] })
    servers.push(server)
    for (const id of ['letta-owner', 'letta-other'])
      await registerMockModelScenario(server.url, id, { steps: [] })
    const configure = (id: string, body: unknown) => fetch(new URL(`/__e2e/letta/quota/${id}`, server.url), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) })
    expect((await configure('letta-owner', { status: 0, body: {} })).status).toBe(400)
    expect((await configure('letta-owner', { status: 200, body: {}, retryAfterSeconds: -1 })).status).toBe(400)
    expect((await configure('letta-owner', { status: 200, body: {} })).status).toBe(200)
    expect((await configure('letta-other', { status: 200, body: {} })).status).toBe(409)
    await removeMockModelScenario(server.url, 'letta-other')
    expect((await fetch(new URL('/__e2e/letta/quota/letta-owner', server.url))).status).toBe(200)
  })
  it('owns a configured response until its scenario is removed', async () => {
    const server = await createMockModelServer({ models: ['letta-quota-test'] })
    servers.push(server)
    await registerMockModelScenario(server.url, 'letta-quota-a', { steps: [] })
    const control = new URL('/__e2e/letta/quota/letta-quota-a', server.url)
    const configured = await fetch(control, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ status: 200, body: { rate_limit: { primary_window: { used_percent: 90 } } } }) })
    expect(configured.status).toBe(200)
    const state = await fetch(control)
    expect(await state.json()).toMatchObject({ scenarioId: 'letta-quota-a', receipts: [] })
    await removeMockModelScenario(server.url, 'letta-quota-a')
    expect((await fetch(control)).status).toBe(404)
  })

  it('allows only one owner when initial configurations overlap', async () => {
    const scenarios = new Set(['letta-race-a', 'letta-race-b'])
    const quota = createScenarioQuota({ hasScenario: id => scenarios.has(id) }, '/__e2e/quota/', 'test account')
    const controls = [
      { scenarioId: 'letta-race-a', owner: 'a' },
      { scenarioId: 'letta-race-b', owner: 'b' },
    ].map(({ scenarioId, owner }) => {
      const request = controlRequest('POST')
      const response = captureResponse()
      const pending = quota.handleControl(request, response.response, new URL(`http://mock.test/__e2e/quota/${scenarioId}`))
      return { scenarioId, owner, request, response, pending }
    })

    for (const { request, owner } of controls) {
      request.push(JSON.stringify({ status: 200, body: { owner } }))
      request.push(null)
    }
    await Promise.all(controls.map(({ pending }) => pending))

    expect(controls.map(({ response }) => response.status()).sort()).toEqual([200, 409])
    const winner = controls.find(({ response }) => response.status() === 200)!
    const loser = controls.find(({ response }) => response.status() === 409)!
    const loserState = captureResponse()
    expect(await quota.handleControl(controlRequest('GET'), loserState.response, new URL(`http://mock.test/__e2e/quota/${loser.scenarioId}`))).toBe(true)
    expect(loserState.status()).toBe(409)

    const firstAnswer = captureResponse()
    quota.answer(firstAnswer.response, true, {}, 'Unauthorized.')
    expect(firstAnswer.status()).toBe(200)
    expect(firstAnswer.body()).toEqual({ owner: winner.owner })

    const update = captureResponse()
    expect(await quota.handleControl(controlRequest('POST', JSON.stringify({ status: 201, body: { owner: `${winner.owner}-updated` } })), update.response, new URL(`http://mock.test/__e2e/quota/${winner.scenarioId}`))).toBe(true)
    expect(update.status()).toBe(200)
    const secondAnswer = captureResponse()
    quota.answer(secondAnswer.response, true, {}, 'Unauthorized.')
    expect(secondAnswer.status()).toBe(201)
    expect(secondAnswer.body()).toEqual({ owner: `${winner.owner}-updated` })

    const winnerState = captureResponse()
    expect(await quota.handleControl(controlRequest('GET'), winnerState.response, new URL(`http://mock.test/__e2e/quota/${winner.scenarioId}`))).toBe(true)
    expect(winnerState.status()).toBe(200)
    expect(winnerState.body()).toMatchObject({ scenarioId: winner.scenarioId, totalRequests: 2, receipts: [{ status: 200 }, { status: 201 }] })
  })

  it('rejects an initial configuration whose scenario is removed during the body read', async () => {
    const scenarioId = 'letta-removed-during-read'
    const scenarios = new Set([scenarioId])
    const quota = createScenarioQuota({ hasScenario: id => scenarios.has(id) }, '/__e2e/quota/', 'test account')
    const url = new URL(`http://mock.test/__e2e/quota/${scenarioId}`)
    const request = controlRequest('POST')
    const configuration = captureResponse()
    const pending = quota.handleControl(request, configuration.response, url)

    scenarios.delete(scenarioId)
    quota.clearScenario(scenarioId)
    request.push(JSON.stringify({ status: 201, body: { owner: 'stale' } }))
    request.push(null)
    expect(await pending).toBe(true)
    expect(configuration.status()).toBe(404)

    scenarios.add(scenarioId)
    const state = captureResponse()
    expect(await quota.handleControl(controlRequest('GET'), state.response, url)).toBe(true)
    expect(state.status()).toBe(404)
    const answer = captureResponse()
    quota.answer(answer.response, true, {}, 'Unauthorized.')
    expect(answer.status()).toBe(404)
  })

  it('rejects a configuration from before its scenario is re-registered', async () => {
    const scenarioId = 'letta-re-registered-during-read'
    const scenarios = new Set([scenarioId])
    const quota = createScenarioQuota({ hasScenario: id => scenarios.has(id) }, '/__e2e/quota/', 'test account')
    const url = new URL(`http://mock.test/__e2e/quota/${scenarioId}`)
    const request = controlRequest('POST')
    const stale = captureResponse()
    const pending = quota.handleControl(request, stale.response, url)

    scenarios.delete(scenarioId)
    quota.clearScenario(scenarioId)
    scenarios.add(scenarioId)
    request.push(JSON.stringify({ status: 201, body: { owner: 'stale' } }))
    request.push(null)
    expect(await pending).toBe(true)
    expect(stale.status()).toBe(409)

    const emptyState = captureResponse()
    expect(await quota.handleControl(controlRequest('GET'), emptyState.response, url)).toBe(true)
    expect(emptyState.status()).toBe(404)
    const staleAnswer = captureResponse()
    quota.answer(staleAnswer.response, true, {}, 'Unauthorized.')
    expect(staleAnswer.status()).toBe(404)

    const fresh = captureResponse()
    expect(await quota.handleControl(controlRequest('POST', JSON.stringify({ status: 202, body: { owner: 'fresh' } })), fresh.response, url)).toBe(true)
    expect(fresh.status()).toBe(200)
    const freshAnswer = captureResponse()
    quota.answer(freshAnswer.response, true, {}, 'Unauthorized.')
    expect(freshAnswer.status()).toBe(202)
    expect(freshAnswer.body()).toEqual({ owner: 'fresh' })
    const freshState = captureResponse()
    expect(await quota.handleControl(controlRequest('GET'), freshState.response, url)).toBe(true)
    expect(freshState.body()).toMatchObject({ scenarioId, totalRequests: 1, receipts: [{ status: 202 }] })
  })

  it('rejects configuration for an unregistered scenario', async () => {
    const server = await createMockModelServer({ models: ['letta-quota-test'] })
    servers.push(server)
    const response = await fetch(new URL('/__e2e/letta/quota/unknown', server.url), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ status: 200, body: {} }) })
    expect(response.status).toBe(404)
  })
})

function controlRequest(method: string, body?: string): IncomingMessage {
  const request = (body === undefined ? new Readable({ read() {} }) : Readable.from([body])) as IncomingMessage
  request.method = method
  return request
}

function captureResponse() {
  let status = 0
  let body: unknown
  const response = {
    setHeader() {
      return response
    },
    writeHead(nextStatus: number) {
      status = nextStatus
      return response
    },
    end(nextBody?: unknown) {
      body = nextBody === undefined ? undefined : JSON.parse(String(nextBody))
      return response
    },
  } as unknown as ServerResponse
  return { response, status: () => status, body: () => body }
}

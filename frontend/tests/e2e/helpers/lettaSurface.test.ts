import { afterEach, describe, expect, it } from 'vitest'
import { LETTA_CHATGPT_ACCESS_TOKEN, LETTA_CHATGPT_ACCOUNT_ID } from './lettaEnvironment'
import { registerMockModelScenario, removeMockModelScenario } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'

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

  it('rejects configuration for an unregistered scenario', async () => {
    const server = await createMockModelServer({ models: ['letta-quota-test'] })
    servers.push(server)
    const response = await fetch(new URL('/__e2e/letta/quota/unknown', server.url), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ status: 200, body: {} }) })
    expect(response.status).toBe(404)
  })
})

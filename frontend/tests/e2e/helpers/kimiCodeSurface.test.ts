import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createKimiCodeSurface, KIMI_CODE_SERVICE_PATH, KIMI_USAGE_CONTROL_PREFIX, kimiPlanUsage } from './kimiCodeSurface'
import { MOCK_IDENTITY_TOKEN } from './mockAgentEnvironment'
import { registerMockModelScenario, removeMockModelScenario } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'

const authorization = { authorization: `Bearer ${MOCK_IDENTITY_TOKEN}` }
const windows = {
  limit_5h: { used_ratio: 0.42, reset_time: '2026-10-25T05:00:00Z' },
  limit_month_total: { used_ratio: 0.1 },
}

describe('createKimiCodeSurface', () => {
  const servers: Awaited<ReturnType<typeof createMockModelServer>>[] = []
  afterEach(async () => {
    await Promise.all(servers.splice(0).map(server => server.close()))
  })

  async function serviceServer(scenarioId: string) {
    const server = await createMockModelServer({ models: ['kimi-service-test'] })
    servers.push(server)
    await registerMockModelScenario(server.url, scenarioId, { steps: [] })
    const control = new URL(`${KIMI_USAGE_CONTROL_PREFIX}${scenarioId}`, server.url)
    const usage = new URL(`${KIMI_CODE_SERVICE_PATH}/usages`, server.url)
    const configure = () => fetch(control, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(kimiPlanUsage(windows)) })
    return { server, control, usage, configure }
  }

  it('answers the scenario usage only to the mocked account, and records each request', async () => {
    const { control, usage, configure } = await serviceServer('kimi-service-usage')
    expect((await fetch(usage, { headers: authorization })).status, 'no answer is configured yet').toBe(404)
    expect((await configure()).status).toBe(200)
    expect((await fetch(usage)).status).toBe(401)
    expect((await fetch(usage, { headers: { authorization: 'Bearer another-token' } })).status).toBe(401)
    expect((await fetch(usage, { method: 'POST', headers: authorization })).status).toBe(405)
    const answered = await fetch(usage, { headers: authorization })
    expect(answered.status).toBe(200)
    expect(await answered.json()).toEqual({ usages: windows })
    const state = await (await fetch(control)).json()
    expect(state.receipts.map((receipt: { status: number, authorized: boolean }) => [receipt.status, receipt.authorized])).toEqual([[401, false], [401, false], [200, true]])
    expect(JSON.stringify(state)).not.toContain(MOCK_IDENTITY_TOKEN)
  })

  it('serves no other route of the service', async () => {
    const { usage, configure } = await serviceServer('kimi-service-routes')
    expect((await configure()).status).toBe(200)
    // The mock answers every `/models` path with its model catalog before a surface sees the request.
    for (const path of [`${KIMI_CODE_SERVICE_PATH}/usages/extra`, `${KIMI_CODE_SERVICE_PATH}/user`, `${KIMI_CODE_SERVICE_PATH}/`]) {
      const response = await fetch(new URL(path, usage), { headers: authorization })
      expect(response.status, path).toBe(404)
      expect(await response.json(), path).toEqual({ error: `The mocked Kimi Code service serves only ${KIMI_CODE_SERVICE_PATH}/usages.` })
    }
  })

  it('forgets the usage of a removed scenario', async () => {
    const { server, control, usage, configure } = await serviceServer('kimi-service-removed')
    expect((await configure()).status).toBe(200)
    await removeMockModelScenario(server.url, 'kimi-service-removed')
    expect((await fetch(control)).status).toBe(404)
    expect((await fetch(usage, { headers: authorization })).status).toBe(404)
  })

  it('refuses a credential when the surface has none', async () => {
    const surface = createKimiCodeSurface({ hasScenario: () => true }, { accessToken: '' })
    const request = new IncomingMessage(new Socket())
    request.method = 'GET'
    request.headers.authorization = 'Bearer '
    const response = new ServerResponse(request)
    const writeHead = vi.spyOn(response, 'writeHead')
    vi.spyOn(response, 'end').mockImplementation(() => response)
    expect(await surface.handleHttp(request, response, new URL(`${KIMI_CODE_SERVICE_PATH}/usages`, 'http://mock.invalid'))).toBe(true)
    expect(writeHead.mock.calls[0]?.[0]).toBe(401)
  })

  it('leaves every other path to the other surfaces', async () => {
    const surface = createKimiCodeSurface({ hasScenario: () => false }, { accessToken: MOCK_IDENTITY_TOKEN })
    for (const path of ['/coding/v1/usages', '/kimi/coding/v1', '/kimi/other', '/v1/chat/completions', '/api/trpc/codingPlans.getUsage']) {
      const request = new IncomingMessage(new Socket())
      request.method = 'GET'
      const response = new ServerResponse(request)
      const end = vi.spyOn(response, 'end').mockImplementation(() => response)
      expect(await surface.handleHttp(request, response, new URL(path, 'http://mock.invalid')), path).toBe(false)
      expect(end).not.toHaveBeenCalled()
    }
  })
})

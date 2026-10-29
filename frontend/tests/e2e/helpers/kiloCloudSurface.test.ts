import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createKiloCloudSurface, KILO_E2E_SUBSCRIPTION, KILO_USAGE_CONTROL_PREFIX, kiloCodingPlanUsage, kiloTrpcResult } from './kiloCloudSurface'
import { MOCK_IDENTITY_TOKEN } from './mockAgentEnvironment'
import { registerMockModelScenario, removeMockModelScenario } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'

const authorization = { authorization: `Bearer ${MOCK_IDENTITY_TOKEN}` }
const window = {
  id: 'five_hour',
  remainingPercent: 58,
  resetsAt: '2026-10-24T13:00:00.000Z',
  period: { unit: 'hour' as const, value: 5 },
}

describe('createKiloCloudSurface', () => {
  const servers: Awaited<ReturnType<typeof createMockModelServer>>[] = []
  afterEach(async () => {
    await Promise.all(servers.splice(0).map(server => server.close()))
  })

  async function cloudServer(scenarioId: string) {
    const server = await createMockModelServer({
      models: ['kilo-cloud-test'],
    })
    servers.push(server)
    await registerMockModelScenario(server.url, scenarioId, { steps: [] })
    const control = new URL(`${KILO_USAGE_CONTROL_PREFIX}${scenarioId}`, server.url)
    const procedure = (name: string, input?: unknown) => {
      const url = new URL(`/api/trpc/${name}`, server.url)
      if (input !== undefined)
        url.searchParams.set('input', JSON.stringify(input))
      return url
    }
    const usage = procedure('codingPlans.getUsage', { subscriptionId: KILO_E2E_SUBSCRIPTION.id })
    const configure = () => fetch(control, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(kiloCodingPlanUsage([window])),
    })
    return {
      server,
      control,
      procedure,
      usage,
      configure,
    }
  }

  it('lists the coding plan only while a scenario owns its usage', async () => {
    const { procedure, configure } = await cloudServer('kilo-cloud-plan')
    const read = async (name: string, input?: unknown) => (await fetch(procedure(name, input), { headers: authorization })).json()
    expect(await read('codingPlans.listSubscriptions')).toEqual(kiloTrpcResult([]))
    expect(await read('byok.list', {})).toEqual(kiloTrpcResult([]))
    expect((await configure()).status).toBe(200)
    expect(await read('codingPlans.listSubscriptions')).toEqual(kiloTrpcResult([
      {
        ...KILO_E2E_SUBSCRIPTION,
        canQueryUsage: true,
        hasInstalledByokKey: true,
        status: 'active',
        cancelAtPeriodEnd: false,
      },
    ]))
    expect(await read('byok.list', {})).toEqual(kiloTrpcResult([
      {
        id: 'leapmux-e2e-key',
        provider_id: 'zai',
        management_source: 'coding_plan',
        is_enabled: true,
      },
    ]))
  })

  it('answers the scenario usage only to the mocked account and its plan, and records each request', async () => {
    const { control, procedure, usage, configure } = await cloudServer('kilo-cloud-usage')
    expect((await fetch(usage, { headers: authorization })).status, 'no answer is configured yet').toBe(404)
    expect((await configure()).status).toBe(200)
    expect((await fetch(usage)).status).toBe(401)
    expect((await fetch(procedure('codingPlans.listSubscriptions'), {
      headers: { authorization: 'Bearer another-token' },
    })).status).toBe(401)
    expect((await fetch(procedure('codingPlans.getUsage', { subscriptionId: 'another-plan' }), { headers: authorization })).status).toBe(400)
    expect((await fetch(procedure('codingPlans.getUsage'), { headers: authorization })).status).toBe(400)
    expect((await fetch(usage, { method: 'POST', headers: authorization })).status).toBe(405)
    const answered = await fetch(usage, { headers: authorization })
    expect(answered.status).toBe(200)
    expect(await answered.json()).toMatchObject(kiloTrpcResult({
      schemaVersion: 1,
      subscription: {
        id: KILO_E2E_SUBSCRIPTION.id,
        windows: [window],
      },
    }))
    const state = await (await fetch(control)).json()
    expect(state.receipts.map((receipt: { status: number, authorized: boolean }) => [receipt.status, receipt.authorized])).toEqual([
      [401, false],
      [200, true],
    ])
    expect(JSON.stringify(state)).not.toContain(MOCK_IDENTITY_TOKEN)
  })

  it('forgets the usage and the plan of a removed scenario', async () => {
    const { server, control, procedure, usage, configure } = await cloudServer('kilo-cloud-removed')
    expect((await configure()).status).toBe(200)
    await removeMockModelScenario(server.url, 'kilo-cloud-removed')
    expect((await fetch(control)).status).toBe(404)
    expect((await fetch(usage, { headers: authorization })).status).toBe(404)
    expect(await (await fetch(procedure('codingPlans.listSubscriptions'), { headers: authorization })).json()).toEqual(kiloTrpcResult([]))
  })

  it('leaves every other path to the other surfaces', async () => {
    const surface = createKiloCloudSurface({
      hasScenario: () => false,
    }, { identityToken: MOCK_IDENTITY_TOKEN })
    for (const path of ['/api/trpc/profile.get', '/api/threads', '/v1/chat/completions']) {
      const request = new IncomingMessage(new Socket())
      request.method = 'GET'
      const response = new ServerResponse(request)
      const end = vi.spyOn(response, 'end').mockImplementation(() => response)
      expect(await surface.handleHttp(request, response, new URL(path, 'http://mock.invalid')), path).toBe(false)
      expect(end).not.toHaveBeenCalled()
    }
  })
})

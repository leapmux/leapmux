import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from './helpers/nativeScenario'
import { describe, expect, it, vi } from 'vitest'
import { MUSE_AGENT } from './muse-code/scenarios'
import './muse-fixtures'

const registered = vi.hoisted(() => ({ fixtures: undefined as Record<string, unknown> | undefined }))
vi.mock('./fixtures', () => ({
  test: {
    extend: (fixtures: Record<string, unknown>) => {
      registered.fixtures = fixtures
      return {}
    },
  },
}))

describe('museTest', () => {
  it('passes its own workspace and provider to the native scenario', async () => {
    const native = registered.fixtures?.native
    if (typeof native !== 'function')
      throw new TypeError('The Muse native fixture was not registered.')
    const page = {} as Page
    const modelScript = {} as ModelScript
    const leapmuxServer: NativeContextFixtures['leapmuxServer'] = { hubUrl: 'http://127.0.0.1:1', adminToken: 'test', workerId: 'worker' }
    let used = false
    await native({
      page,
      modelScript,
      leapmuxServer,
      providerWorkspace: { workspaceId: 'muse-workspace' },
    }, async (context: ManagedNativeScenarioContext) => {
      used = true
      expect(context.page).toBe(page)
      expect(context.modelScript).toBe(modelScript)
      expect(context.leapmuxServer).toBe(leapmuxServer)
      expect(context.workspaceId).toBe('muse-workspace')
      expect(context.providerAgent).toBe(MUSE_AGENT)
      expect(context.provider).toBe(MUSE_AGENT.provider)
      expect(context.resolveToolRowId).toBeTypeOf('function')
    })
    expect(used).toBe(true)
  })
})

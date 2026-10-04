import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativeWorkspaceTrustLimit } from './nativeWorkspaceTrustLimit'

describe('exerciseNativeWorkspaceTrustLimit', () => {
  it.each([undefined, {}, { projectConfiguration: {} }, { projectConfiguration: { prepare() {} } }])('rejects a missing actual configuration proof before browser access: %j', async (options) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
      workspaceId: 'config-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The configuration boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The configuration boundary must run before model access.')
      },
    }
    await expect(Reflect.apply(exerciseNativeWorkspaceTrustLimit, undefined, [context, options])).rejects.toThrow('requires an actual native project configuration proof')
  })

  it.each([
    { startup: 'other' },
    { startup: 'failed' },
    { startup: 'failed', startupError: '' },
    { startup: 'failed', startupError: ' \n\t' },
  ])('rejects an invalid failure proof before configuration or browser access: %j', async (startup) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
      workspaceId: 'failed-config-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The failure boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The failure boundary must run before model access.')
      },
    }
    const options = {
      ...startup,
      projectConfiguration: {
        prepare() { throw new Error('The failure boundary must run before configuration writes.') },
        async prove() { throw new Error('The failure boundary must run before configuration proof.') },
      },
    }
    await expect(Reflect.apply(exerciseNativeWorkspaceTrustLimit, undefined, [context, options])).rejects.toThrow(/workspace trust startup|requires the native configuration error/)
  })
})

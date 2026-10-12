import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanupOnFailure, finishCleanup, withCleanup } from '../helpers/cleanup'
import { MOCK_MODEL_IDS } from '../helpers/mockAgentEnvironment'
import { createMockModelServer } from '../helpers/mockModelServer'
import { startModelScript } from '../helpers/modelScriptFixture'
import { registerDeepseekHarnessChildReport } from './childReportRegistration'

const cleanup: (() => Promise<void>)[] = []

afterEach(async () => {
  await finishCleanup(cleanup.splice(0).map(finish => finish()))
})

async function script() {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  const lifecycle = await cleanupOnFailure(() => startModelScript(server.url), () => server.close())
  cleanup.push(() => withCleanup(() => lifecycle.finish(false), () => server.close()))
  return lifecycle.script
}

describe('registerDeepseekHarnessChildReport', () => {
  it('reuses a registered native child rule before the rule matches a request', async () => {
    const modelScript = await script()
    const first = await registerDeepseekHarnessChildReport(modelScript, 'native-child-session')
    expect((await modelScript.status()).ruleMatches).not.toHaveProperty(first)
    await expect(registerDeepseekHarnessChildReport(modelScript, 'native-child-session')).resolves.toBe(first)
  })

  it('shares concurrent registration for the exact child in one scenario', async () => {
    const modelScript = await script()
    await expect(Promise.all([
      registerDeepseekHarnessChildReport(modelScript, 'native-child-session'),
      registerDeepseekHarnessChildReport(modelScript, 'native-child-session'),
    ])).resolves.toEqual([
      'the native parent report for native-child-session',
      'the native parent report for native-child-session',
    ])
  })

  it('keeps different children and different scenarios separate', async () => {
    const first = await script()
    const second = await script()
    const firstRule = await registerDeepseekHarnessChildReport(first, 'native-child-session')
    expect(await registerDeepseekHarnessChildReport(first, 'another-child-session')).not.toBe(firstRule)
    expect(await registerDeepseekHarnessChildReport(second, 'native-child-session')).toBe(firstRule)
  })

  it('removes a failed pending registration before a later explicit call', async () => {
    const modelScript = await script()
    const failure = new Error('The native report registration request failed.')
    const register = vi.spyOn(modelScript, 'rule').mockRejectedValueOnce(failure)
    await expect(registerDeepseekHarnessChildReport(modelScript, 'native-child-session')).rejects.toBe(failure)
    const registered = await registerDeepseekHarnessChildReport(modelScript, 'native-child-session')
    expect(await registerDeepseekHarnessChildReport(modelScript, 'native-child-session')).toBe(registered)
    expect(register).toHaveBeenCalledTimes(2)
  })

  it.each(['', 'native\0child'])('rejects an invalid child identity before registration: %j', async (id) => {
    const modelScript = await script()
    const register = vi.spyOn(modelScript, 'rule')
    await expect(registerDeepseekHarnessChildReport(modelScript, id)).rejects.toThrow('exact native Session identity')
    expect(register).not.toHaveBeenCalled()
  })
})

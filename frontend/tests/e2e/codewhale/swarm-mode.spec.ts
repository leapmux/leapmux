import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { getGlobalState } from '../helpers/server'
import { hasCodewhaleOperateContract, readCodewhaleNativeMode } from './nativeMode'

codewhaleTest('applies native Operate orchestration and retains its mode after reload', async ({ native }, testInfo) => {
  await exerciseNativeOptionSequence(native, {
    groupId: 'codewhale_mode',
    steps: [
      { value: 'agent', via: 'default' },
      { value: 'operate', via: 'choose' },
      { value: 'operate', via: 'reload' },
      { value: 'agent', via: 'choose' },
    ],
    nativeProof: async (request, step, index) => {
      const agent = await currentNativeAgent(native)
      const home = native.leapmuxServer.agentEnv?.CODEWHALE_HOME
      if (!home)
        throw new Error('The native Codewhale mode proof has no private home.')
      const receipt = readCodewhaleNativeMode({ home, runDir: getGlobalState().tmpDir, threadId: agent.agentSessionId, workingDir: agent.workingDir })
      expect(receipt.mode).toBe(step.value)
      expect(hasCodewhaleOperateContract(request)).toBe(index > 0)
      await testInfo.attach(`codewhale-native-mode-${index}`, { body: JSON.stringify({ receipt, request }), contentType: 'application/json' })
    },
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/523.

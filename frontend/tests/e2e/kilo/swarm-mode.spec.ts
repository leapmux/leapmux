import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, expectSameNativeSession, nativeModelToolNames, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { kiloTest } from '../kilo-fixtures'

/** The tools of Kilo Swarm, the shared agent board. */
const KILO_SWARM_TOOLS = ['board_post', 'board_read']

// Kilo reads its Swarm switch only at start, so each change restarts the agent in the same session.
kiloTest('changes the native Kilo Swarm tools across same-session restarts and reload', async ({ native }) => {
  const { page } = native
  const contextPrompt = 'Remember this conversation before changing Kilo Swarm.'
  const contextAnswer = 'KILO_SWARM_CONTEXT'
  const seeded = await sendNativeAnswer(native, contextPrompt, contextAnswer)
  expect(nativeModelToolNames(seeded)).toEqual(expect.arrayContaining(KILO_SWARM_TOOLS))
  const before = await currentNativeAgent(native)
  const unchanged = {
    model: nativeOptionValue(before, 'model'),
    primaryAgent: nativeOptionValue(before, 'primaryAgent'),
  }
  await expectSettingsOptionsOffered(page, 'kiloSwarm', ['false', 'true'])
  await exerciseNativeOptionSequence(native, {
    groupId: 'kiloSwarm',
    steps: [
      { value: 'true', via: 'default' },
      { value: 'false', via: 'choose' },
      { value: 'true', via: 'choose' },
      { value: 'false', via: 'choose' },
      { value: 'false', via: 'reload' },
    ],
    nativeProof: async (request, step) => {
      const tools = nativeModelToolNames(request)
      if (step.value === 'true')
        expect(tools).toEqual(expect.arrayContaining(KILO_SWARM_TOOLS))
      else
        expect(tools.filter(name => name.startsWith('board_'))).toEqual([])
      // The catalog keeps Kilo's other tools, so an absent board is not an absent catalog.
      expect(tools).toContain('task')
      expect(JSON.stringify(request.body)).toContain(contextPrompt)
      expect(JSON.stringify(request.body)).toContain(contextAnswer)
      const current = await currentNativeAgent(native)
      expectSameNativeSession(before, current, 'Changing Kilo Swarm')
      expect(nativeOptionValue(current, 'kiloSwarm')).toBe(step.value)
      for (const [groupId, value] of Object.entries(unchanged))
        expect(nativeOptionValue(current, groupId)).toBe(value)
    },
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/523.

import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, expectSameNativeSession, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { offeredTools } from './offeredTools'
import { CLINE_TEAM_TOOL_NAMES } from './toolCatalog'

clineTest('changes native Agent Teams tools across same-session rebuilds and reload', async ({ native }) => {
  const { page } = native
  const contextPrompt = 'Remember this conversation before changing Agent Teams.'
  const contextAnswer = 'CLINE_AGENT_TEAMS_CONTEXT'
  const seeded = await sendNativeAnswer(native, contextPrompt, contextAnswer)
  expect(offeredTools(seeded.body)).toEqual(expect.arrayContaining([...CLINE_TEAM_TOOL_NAMES]))
  const before = await currentNativeAgent(native)
  const unchanged = {
    model: nativeOptionValue(before, 'model'),
    effort: nativeOptionValue(before, 'effort'),
    thinking: nativeOptionValue(before, 'thinking'),
    permissionMode: nativeOptionValue(before, 'permissionMode'),
  }
  await expectSettingsOptionsOffered(page, 'agentTeams', ['false', 'true'])
  await exerciseNativeOptionSequence(native, {
    groupId: 'agentTeams',
    steps: [
      { value: 'true', via: 'default' },
      { value: 'false', via: 'choose' },
      { value: 'true', via: 'choose' },
      { value: 'false', via: 'choose' },
      { value: 'false', via: 'reload' },
    ],
    nativeProof: async (request, step) => {
      const tools = offeredTools(request.body)
      if (step.value === 'true')
        expect(tools).toEqual(expect.arrayContaining([...CLINE_TEAM_TOOL_NAMES]))
      else
        expect(tools.filter(name => name.startsWith('team_'))).toEqual([])
      expect(tools).toContain('spawn_agent')
      expect(JSON.stringify(request.body)).toContain(contextPrompt)
      expect(JSON.stringify(request.body)).toContain(contextAnswer)
      const current = await currentNativeAgent(native)
      expectSameNativeSession(before, current, 'Changing Agent Teams')
      expect(nativeOptionValue(current, 'agentTeams')).toBe(step.value)
      for (const [groupId, value] of Object.entries(unchanged))
        expect(nativeOptionValue(current, groupId)).toBe(value)
    },
  })
})

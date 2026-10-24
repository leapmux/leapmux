import type { Page } from '@playwright/test'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { codebuddyTest } from '../codebuddy-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { createCodebuddyEnvironment } from '../helpers/codebuddyEnvironment'
import { CODEBUDDY_ALT_MODEL_ID, CODEBUDDY_ALT_MODEL_WIRE_ID, CODEBUDDY_MODEL_ID, MOCK_MODELS, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, expectSameNativeSession, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsIdle } from '../helpers/ui'
import { CODEBUDDY_AGENT, nativeContext } from './scenarios'

async function withThinkingWorkspace(
  fixtures: { page: Page, leapmuxServer: ServerInfo, modelScript: ModelScript },
  options: { profileThinking: boolean },
  use: (native: ManagedNativeScenarioContext, profile: string) => Promise<void>,
): Promise<void> {
  const { page, leapmuxServer, modelScript } = fixtures
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'codebuddy-thinking',
    workerName: 'CodeBuddy thinking test',
    providerAgent: CODEBUDDY_AGENT,
    prepare: (directory) => {
      const home = join(directory, 'home')
      const environment = createCodebuddyEnvironment({
        homeDir: home,
        baseURL: `${leapmuxServer.mockModelUrl}/v1`,
        modelKey: MODEL_KEY,
        modelID: MOCK_MODELS.deepseek,
        alternateModelID: CODEBUDDY_ALT_MODEL_WIRE_ID,
        startModel: CODEBUDDY_MODEL_ID,
        thinkingEnabled: options.profileThinking,
        primaryReasoning: { supported: true, defaultEffort: 'medium' },
        alternateReasoning: { supported: false },
      })
      return {
        agentEnv: { ...leapmuxServer.agentEnv, ...environment, HOME: home, USERPROFILE: home, ZDOTDIR: home },
        setup: { profile: join(home, '.codebuddy', 'settings.json') },
      }
    },
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(CODEBUDDY_AGENT.provider, { optionValues: { effort: 'high' } })),
  }, async ({ server, workspaceId, setup }) => {
    await use(await nativeContext({ page, leapmuxServer: server, workspaceId, modelScript }), setup.profile)
  })
}

function expectThinking(request: MockModelRequestRecord, enabled: boolean, model: string = MOCK_MODELS.deepseek): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toHaveProperty('model', model)
  if (enabled)
    expect(request.body).toHaveProperty('reasoning_effort', 'high')
  else
    expect(request.body).not.toHaveProperty('reasoning_effort')
}

function expectConciseInstructions(request: MockModelRequestRecord): void {
  if (!isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The native CodeBuddy request has no messages.')
  const instructions = request.body.messages.filter(isObject).filter(message => message.role === 'system' || message.role === 'developer')
  expect(JSON.stringify(instructions)).toContain('# Output Style: Concise')
}

codebuddyTest('changes native thinking before the first prompt and preserves the chosen effort after reload', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { profileThinking: false }, async (native, profile) => {
    const before = await currentNativeAgent(native)
    await exerciseNativeOptionSequence(native, {
      groupId: 'thinking',
      steps: [
        { value: 'true', via: 'choose' },
        { value: 'false', via: 'choose' },
        { value: 'true', via: 'choose' },
        { value: 'true', via: 'reload' },
      ],
      nativeProof: async (request, step) => {
        expectThinking(request, step.value === 'true')
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'A live thinking change')
        expect(nativeOptionValue(current, 'effort')).toBe('high')
        expect(JSON.parse(readFileSync(profile, 'utf8'))).toHaveProperty('alwaysThinkingEnabled', false)
      },
    })
  })
})

codebuddyTest('restores the native profile for Auto without losing conversation context', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { profileThinking: false }, async (native, profile) => {
    const baseline = 'THINKINGPROFILECONTEXTPROOF'
    expectThinking(await sendNativeAnswer(native, 'Remember this conversation before thinking changes.', baseline), false)
    const before = await currentNativeAgent(native)
    await exerciseNativeOptionSequence(native, {
      groupId: 'thinking',
      steps: [{ value: 'true', via: 'choose' }, { value: 'false', via: 'choose' }, { value: 'auto', via: 'choose' }, { value: 'auto', via: 'reload' }],
      nativeProof: async (request, step) => {
        expectThinking(request, step.value === 'true')
        expect(JSON.stringify(request.body)).toContain(baseline)
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Restoring the thinking profile')
        expect(nativeOptionValue(current, 'effort')).toBe('high')
        expect(JSON.parse(readFileSync(profile, 'utf8'))).toHaveProperty('alwaysThinkingEnabled', false)
      },
    })
  })
})

codebuddyTest('preserves an explicit Off override through a style restart and restores a true profile for Auto', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { profileThinking: true }, async (native, profile) => {
    expectThinking(await sendNativeAnswer(native, 'Reply using the configured thinking profile.', 'The configured profile answered.'), true)
    const before = await currentNativeAgent(native)
    await chooseSettingsOption(page, 'thinking-false')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'thinking-false')
    await chooseSettingsOption(page, 'outputStyle-Concise')
    await waitForSettingsIdle(page)
    await exerciseNativeOptionSequence(native, {
      groupId: 'thinking',
      steps: [{ value: 'false', via: 'default' }, { value: 'true', via: 'choose' }, { value: 'auto', via: 'choose' }, { value: 'auto', via: 'reload' }],
      nativeProof: async (request, step) => {
        expectThinking(request, step.value !== 'false')
        expectConciseInstructions(request)
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Combining thinking and output style')
        expect(nativeOptionValue(current, 'outputStyle')).toBe('Concise')
        expect(nativeOptionValue(current, 'effort')).toBe('high')
        expect(JSON.parse(readFileSync(profile, 'utf8'))).toHaveProperty('alwaysThinkingEnabled', true)
      },
    })
  })
})

codebuddyTest('retains the thinking preference through a model without reasoning and back', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { profileThinking: false }, async (native) => {
    await chooseSettingsOption(page, 'thinking-true')
    await waitForSettingsIdle(page)
    expectThinking(await sendNativeAnswer(native, 'Reply with thinking enabled.', 'Thinking is configured.'), true)
    const before = await currentNativeAgent(native)
    await exerciseNativeOptionSequence(native, {
      groupId: 'model',
      steps: [{ value: CODEBUDDY_ALT_MODEL_ID, via: 'choose' }, { value: CODEBUDDY_ALT_MODEL_ID, via: 'reload' }, { value: CODEBUDDY_MODEL_ID, via: 'choose' }],
      nativeProof: async (request, step) => {
        const capable = step.value === CODEBUDDY_MODEL_ID
        expectThinking(request, capable, capable ? MOCK_MODELS.deepseek : CODEBUDDY_ALT_MODEL_WIRE_ID)
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Switching models with a thinking preference')
        expect(nativeOptionValue(current, 'thinking')).toBe('true')
        expect(nativeOptionValue(current, 'effort')).toBe('high')
      },
    })
  })
})

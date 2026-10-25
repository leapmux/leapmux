import type { Page } from '@playwright/test'
import type { ServerInfo } from '../fixtures'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { clineTest } from '../cline-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { createClineEnvironment } from '../helpers/clineEnvironment'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, expectSameNativeSession, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { queuedInputRow } from '../helpers/steer'
import { chooseSettingsOption, inputQueue, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { CLINE_AGENT, nativeContext } from './scenarios'

async function withThinkingWorkspace(
  fixtures: { page: Page, leapmuxServer: ServerInfo, modelScript: ModelScript },
  options: { thinking: 'auto' | 'false' | 'true', effort: 'auto' | 'low' | 'high', profileEffort?: 'low' },
  use: (native: ManagedNativeScenarioContext, assertProfileUnchanged: () => void) => Promise<void>,
): Promise<void> {
  const { page, leapmuxServer, modelScript } = fixtures
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'cline-thinking',
    workerName: 'Cline thinking test',
    providerAgent: CLINE_AGENT,
    prepare: (directory) => {
      const home = join(directory, 'home')
      const environment = createClineEnvironment({
        homeDir: home,
        baseURL: `${leapmuxServer.mockModelUrl}/v1`,
        modelKey: MODEL_KEY,
        providerID: 'deepseek',
        modelID: 'deepseek-v4-pro',
        ...(options.profileEffort === undefined ? {} : { reasoning: { enabled: true, effort: options.profileEffort } }),
      })
      const profile = join(home, '.cline', 'data', 'settings', 'providers.json')
      return {
        agentEnv: { ...leapmuxServer.agentEnv, ...environment, HOME: home, USERPROFILE: home, ZDOTDIR: home },
        setup: { profile, originalProfile: readFileSync(profile, 'utf8') },
      }
    },
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(CLINE_AGENT.provider, { optionValues: { thinking: options.thinking, effort: options.effort } })),
  }, async ({ server, workspaceId, setup }) => {
    const assertProfileUnchanged = () => expect(readFileSync(setup.profile, 'utf8')).toBe(setup.originalProfile)
    await use(await nativeContext({ page, leapmuxServer: server, workspaceId, modelScript }), assertProfileUnchanged)
    assertProfileUnchanged()
  })
}

function expectNativeThinking(request: MockModelRequestRecord, thinking: 'off' | 'low' | 'high', model: string = 'deepseek-v4-pro'): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toHaveProperty('model', model)
  if (thinking === 'off') {
    expect(request.body).toHaveProperty('thinking.type', 'disabled')
    expect(request.body).not.toHaveProperty('reasoning_effort')
  }
  else {
    expect(request.body).toHaveProperty('reasoning_effort', thinking)
    expect(request.body).not.toHaveProperty('thinking.type', 'disabled')
  }
}

clineTest('starts with thinking Off and retains the chosen effort through live changes and reload', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { thinking: 'false', effort: 'low' }, async (native, assertProfileUnchanged) => {
    const before = await currentNativeAgent(native)
    await exerciseNativeOptionSequence(native, {
      groupId: 'thinking',
      steps: [{ value: 'false', via: 'default' }, { value: 'true', via: 'choose' }, { value: 'false', via: 'choose' }],
      nativeProof: async (request, step) => {
        expectNativeThinking(request, step.value === 'false' ? 'off' : 'low')
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Changing the thinking preference')
        expect(nativeOptionValue(current, 'effort')).toBe('low')
        assertProfileUnchanged()
      },
    })
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    expectNativeThinking(await sendNativeAnswer(native, 'Reply with thinking still disabled.', 'The effort preference changed while thinking stayed disabled.'), 'off')
    expect(nativeOptionValue(await currentNativeAgent(native), 'effort')).toBe('high')
    await exerciseNativeOptionSequence(native, {
      groupId: 'thinking',
      steps: [{ value: 'true', via: 'choose' }, { value: 'true', via: 'reload' }],
      nativeProof: async (request) => {
        expectNativeThinking(request, 'high')
        expectSameNativeSession(before, await currentNativeAgent(native), 'Restoring the chosen effort')
        assertProfileUnchanged()
      },
    })
  })
})

clineTest('restores configured effort for On and Auto without losing the session or conversation', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { thinking: 'auto', effort: 'auto', profileEffort: 'low' }, async (native, assertProfileUnchanged) => {
    const contextMarker = 'CLINE_THINKING_PROFILE_CONTEXT'
    expectNativeThinking(await sendNativeAnswer(native, 'Remember this before changing thinking.', contextMarker), 'low')
    const before = await currentNativeAgent(native)
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    expectNativeThinking(await sendNativeAnswer(native, 'Reply with the selected high effort.', 'High effort is selected.'), 'high')
    await chooseSettingsOption(page, 'thinking-false')
    await waitForSettingsIdle(page)
    expectNativeThinking(await sendNativeAnswer(native, 'Reply with thinking disabled.', 'Thinking is disabled.'), 'off')
    await chooseSettingsOption(page, 'effort-auto')
    await waitForSettingsIdle(page)
    expectNativeThinking(await sendNativeAnswer(native, 'Keep thinking disabled with configured effort.', 'The configured effort remains disabled.'), 'off')
    await exerciseNativeOptionSequence(native, {
      groupId: 'thinking',
      steps: [{ value: 'true', via: 'choose' }, { value: 'auto', via: 'choose' }, { value: 'auto', via: 'reload' }],
      nativeProof: async (request) => {
        expectNativeThinking(request, 'low')
        expect(JSON.stringify(request.body)).toContain(contextMarker)
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Restoring the configured reasoning defaults')
        expect(nativeOptionValue(current, 'effort')).toBe('auto')
        assertProfileUnchanged()
      },
    })
  })
})

clineTest('preserves explicit thinking and effort through native mode rebuilds and model changes', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { thinking: 'true', effort: 'high' }, async (native, assertProfileUnchanged) => {
    expectNativeThinking(await sendNativeAnswer(native, 'Reply with the initial explicit thinking settings.', 'The initial settings reached the model.'), 'high')
    const before = await currentNativeAgent(native)
    await exerciseNativeOptionSequence(native, {
      groupId: 'permissionMode',
      steps: [{ value: 'plan', via: 'choose' }, { value: 'act', via: 'choose' }],
      nativeProof: async (request) => {
        expectNativeThinking(request, 'high')
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Rebuilding the native mode')
        expect(nativeOptionValue(current, 'thinking')).toBe('true')
        expect(nativeOptionValue(current, 'effort')).toBe('high')
        assertProfileUnchanged()
      },
    })
    await exerciseNativeOptionSequence(native, {
      groupId: 'model',
      steps: [{ value: 'deepseek-flash', via: 'choose' }, { value: 'deepseek-v4-pro', via: 'choose' }, { value: 'deepseek-v4-pro', via: 'reload' }],
      nativeProof: async (request, step) => {
        expectNativeThinking(request, 'high', step.value)
        const current = await currentNativeAgent(native)
        expectSameNativeSession(before, current, 'Switching the native model')
        expect(nativeOptionValue(current, 'thinking')).toBe('true')
        expect(nativeOptionValue(current, 'effort')).toBe('high')
        assertProfileUnchanged()
      },
    })
  })
})

clineTest('keeps queued input behind a pending reasoning reset until the active turn finishes', async ({ page, leapmuxServer, modelScript }) => {
  await withThinkingWorkspace({ page, leapmuxServer, modelScript }, { thinking: 'true', effort: 'high', profileEffort: 'low' }, async (native, assertProfileUnchanged) => {
    const before = await currentNativeAgent(native)
    const gate = 'cline-thinking-active-answer'
    const firstAnswer = 'CLINE_THINKING_HELD_CONTEXT'
    const nextPrompt = 'Reply after restoring the configured reasoning defaults.'
    const start = await modelScript.queue({ text: firstAnswer, gate }, { text: 'The queued answer used the configured defaults.' })
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt('Reply using the current explicit reasoning settings.'))
      await modelScript.waitForGate(gate)
      expectNativeThinking(await modelScript.requestAt(start), 'high')
      await chooseSettingsOption(page, 'effort-auto')
      await chooseSettingsOption(page, 'thinking-auto')
      await expect(page.locator('[data-testid="settings-loading-spinner"]')).toBeVisible()
      await sendMessage(page, modelScript.prompt(nextPrompt))
      await expect(queuedInputRow(page, nextPrompt)).toBeVisible()
      const status = await modelScript.status()
      expect(status.pendingGates).toContain(gate)
      expect(status.nextStep).toBe(start + 1)
    }, async () => { await modelScript.releaseGateIfHeld(gate) })
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await waitForSettingsIdle(page)
    await expect(inputQueue(page)).toHaveCount(0)
    const next = await modelScript.requestAt(start + 1)
    expectNativeThinking(next, 'low')
    expect(JSON.stringify(next.body)).toContain(firstAnswer)
    const current = await currentNativeAgent(native)
    expectSameNativeSession(before, current, 'Applying the deferred reasoning reset')
    expect(nativeOptionValue(current, 'thinking')).toBe('auto')
    expect(nativeOptionValue(current, 'effort')).toBe('auto')
    assertProfileUnchanged()
  })
})

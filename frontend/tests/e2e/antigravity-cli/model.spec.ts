import { expect } from '@playwright/test'
import { ACCOUNT_DEFAULT_MODEL, EFFORT_AUTO } from '../../../src/generated/contracts/worker-vocab'
import { antigravityTest } from '../antigravity-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText, nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { uniqueMarker } from '../helpers/shellArguments'
import { closeComposerMenus, expectSettingsOptionChosen, offeredSettingsOptions, openSettingsMenu, waitForNativeSettingsHydrated } from '../helpers/ui'
import { nativeContext } from './scenarios'

antigravityTest('changes the native model and restores the selected model after reload', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  const menu = await openSettingsMenu(native.page, 'model')
  await expect(menu.getByTestId('model-gemini-3.8-flash').getByText('Gemini 3.8 Flash', { exact: true })).toBeVisible()
  await expect(menu.getByTestId('model-gemini-4-neon').getByText('Gemini 4 Neon', { exact: true })).toBeVisible()
  await closeComposerMenus(native.page)
  const before = await currentNativeAgent(native)
  const marker = uniqueMarker('GEMINIMODELCONTEXT')
  expect(nativeOptionValue(before, 'model')).toBe('gemini-3.8-flash')
  const baseline = await sendNativeAnswer(native, `Preserve ${marker} before the model change.`, `The previous model preserved ${marker}.`)
  expect(baseline.protocol).toBe('google-generative-language')
  expect(baseline.path).toContain('/models/gemini-3.8-flash:')
  const baselineModel = /\/models\/([^:]+):/.exec(baseline.path)?.[1]
  expect(baselineModel).toBeDefined()
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: 'gemini-3.1-pro',
    nativeProof: async (request) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.path).toContain('/models/gemini-3.1-pro-preview:')
      expect(/\/models\/([^:]+):/.exec(request.path)?.[1]).not.toBe(baselineModel)
      expect(nativeModelContextText(request)).toContain(`Preserve ${marker} before the model change.`)
      expect(nativeModelContextText(request)).toContain(`The previous model preserved ${marker}.`)
      const current = await currentNativeAgent(native)
      expect(current.id).toBe(before.id)
      expect(current.agentSessionId).toBe(before.agentSessionId)
    },
  })
})

antigravityTest('resolves the account default launch to a concrete model and auto effort', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(native, {
    overrides: {
      model: ACCOUNT_DEFAULT_MODEL,
      optionValues: { effort: EFFORT_AUTO },
    },
  })
  await waitForNativeSettingsHydrated(native.page)
  const agent = await currentNativeAgent(native)
  const settledModel = nativeOptionValue(agent, 'model')
  expect(settledModel).toBeTruthy()
  expect(settledModel).not.toBe(ACCOUNT_DEFAULT_MODEL)
  const modelIds = nativeOptionGroup(agent, 'model')?.options.map(option => option.id) ?? []
  expect(modelIds).toContain(settledModel)
  expect(modelIds).not.toContain(ACCOUNT_DEFAULT_MODEL)

  const offeredModels = await offeredSettingsOptions(native.page, 'model')
  expect(offeredModels).toContain(settledModel)
  expect(offeredModels).not.toContain(ACCOUNT_DEFAULT_MODEL)
  await expectSettingsOptionChosen(native.page, `model-${settledModel}`)

  expect(nativeOptionValue(agent, 'effort')).toBe(EFFORT_AUTO)
  await expectSettingsOptionChosen(native.page, `effort-${EFFORT_AUTO}`)

  const request = await sendNativeAnswer(native, 'Reply on the default Antigravity model.', 'The default concrete model answered.')
  expect(request.protocol).toBe('google-generative-language')
  const wireModel = /\/models\/([^:]+):/.exec(request.path)?.[1]
  expect(wireModel === settledModel || wireModel?.startsWith(`${settledModel}-`)).toBe(true)
})

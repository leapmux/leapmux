import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_PROXY_PROVIDER, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent, nativeOptionGroup } from '../helpers/nativeScenario'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, waitForNativeSettingsHydrated } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

/** A proxy model whose ladder lacks None, which `MOCK_MODELS.openai` offers through the proxy. */
const JUNIE_MODEL_WITHOUT_NONE = 'gpt-6-astra'

/** Open the model picker of the composer and return its visible popover. */
async function openModelPicker(page: Page) {
  await page.locator('[data-testid="composer-model-trigger"]:visible').click()
  return page.locator('[data-testid="composer-model-popover"]:visible')
}

/** Require the request of the Responses route of the proxy model `model` at `effort`. */
function expectProxyRequest(request: MockModelRequestRecord, model: string, effort: string): void {
  expect(request.path).toBe('/v1/responses')
  expect(request.body).toMatchObject({ model, reasoning: { effort } })
}

/** Require the request of the Responses route of the proxy model at the low effort. */
function expectLowEffortRequest(request: MockModelRequestRecord): void {
  expectProxyRequest(request, JUNIE_NATIVE_EFFORT_MODEL, 'low')
}

/**
 * Answer the turns that Junie runs beside each user turn of an effort spec: its capability filter, its language
 * check, and its prediction of the next prompt.
 */
async function answerJunieEffortHousekeeping(modelScript: ModelScript): Promise<void> {
  await modelScript.rule(
    { name: 'junie-effort-capability-filter', when: { system: 'classify a user request and route it to the correct handler' }, respond: { text: 'CODE' } },
    { name: 'junie-effort-language', when: { system: 'You are a language identification utility.' }, respond: { text: JSON.stringify({ iso: 'en', confidence: 1 }) } },
    { name: 'junie-effort-next-prompt', when: { user: 'Return ONLY the predicted prompt \\(max 7 tokens\\)' }, respond: { text: 'NONE' } },
  )
}

/**
 * A proof that waits, after the first answered turn, for Junie's one prediction of the next prompt. A restore step
 * reloads the page next, and the wait keeps the reload from interrupting the prediction.
 */
function afterFirstPrediction(modelScript: ModelScript, proof: (request: MockModelRequestRecord) => void): (request: MockModelRequestRecord) => Promise<void> {
  let answeredTurns = 0
  return async (request) => {
    proof(request)
    answeredTurns++
    if (answeredTurns === 1)
      await expect.poll(async () => (await modelScript.status()).ruleMatches['junie-effort-next-prompt'] ?? 0).toBe(1)
  }
}

/** Build the scenario context of the native-effort workspace, with the housekeeping answers of an effort spec. */
async function nativeEffortContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  const context = await nativeContext(fixtures)
  await answerJunieEffortHousekeeping(fixtures.modelScript)
  return context
}

/** The option value of the proxy model `model` in the live catalog. The value carries a prefix that Junie chooses. */
async function proxyModelOption(context: ManagedNativeScenarioContext, model: string): Promise<string> {
  await waitForNativeSettingsHydrated(context.page)
  const suffix = `proxy:${JUNIE_PROXY_PROVIDER}:${model}`
  const options = nativeOptionGroup(await currentNativeAgent(context), 'model')?.options.map(option => option.id) ?? []
  const matches = options.filter(option => option.endsWith(suffix))
  expect(matches, `the live catalog offers one proxy model ${model}`).toHaveLength(1)
  return matches[0]!
}

junieTest.describe('Junie settings', () => {
  junieTest('an effort switch reaches the next request and survives a reload', async ({ authenticatedNativeEffortJunieWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedNativeEffortJunieWorkspace.workspaceId })
    let proxyModelOptionID = ''
    await exerciseNativeOption(context, {
      groupId: 'effort',
      value: 'low',
      // The prepare step runs on the live catalog, so the picker lists the proxy models that Junie reports.
      prepare: async () => {
        const proxyModel = (await openModelPicker(page)).getByRole('option', { name: 'GPT-5.3-codex', exact: true })
        await expect(proxyModel).toHaveCount(1)
        await expect(proxyModel).toHaveAttribute('aria-selected', 'true')
        proxyModelOptionID = await proxyModel.getAttribute('data-testid') ?? ''
        if (!proxyModelOptionID)
          throw new Error('Junie did not identify the native proxy model option')
        expect(proxyModelOptionID).toContain(`proxy:${JUNIE_PROXY_PROVIDER}:${JUNIE_NATIVE_EFFORT_MODEL}`)
        await closeComposerMenus(page)
        await answerJunieEffortHousekeeping(modelScript)
      },
      nativeProof: afterFirstPrediction(modelScript, expectLowEffortRequest),
    })
    // The restore keeps the proxy model, not only the effort. An open picker keeps the list that it showed when it
    // opened, so the picker opens on the live catalog.
    await waitForNativeSettingsHydrated(page)
    await expect((await openModelPicker(page)).getByTestId(proxyModelOptionID)).toHaveAttribute('aria-selected', 'true')
    await closeComposerMenus(page)
  })
})

// Junie applies an effort through a relaunch with `--effort` (`junie/settings.go`, `effortNeedsRestart`), and a later
// model write keeps that launch effort when the new model offers it. The native-effort workspace opens at High, so
// Low differs from the level at which the session started.
junieTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedNativeEffortJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeEffortContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedNativeEffortJunieWorkspace.workspaceId })
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: await proxyModelOption(context, JUNIE_MODEL_WITHOUT_NONE),
    nativeProof: afterFirstPrediction(modelScript, request => expectProxyRequest(request, JUNIE_MODEL_WITHOUT_NONE, 'low')),
  })
})

// Every model that Junie offers has an effort ladder: each proxy model has its own, and a custom profile states no
// effort field, so Junie gives it High, Medium, and Low. No model hides the control. The proxy ladders differ, so a
// round trip can pass through a model without the chosen level: gpt-6-astra lacks None. Junie runs that model at Low,
// the lowest level that it offers, and keeps Low when the model returns.
junieTest('settles a defined effort after a round trip through a model without the chosen level', async ({ authenticatedNativeEffortJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeEffortContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedNativeEffortJunieWorkspace.workspaceId })
  await exerciseEffortModelRoundTrip(context, {
    effortGroupId: 'effort',
    model: await proxyModelOption(context, MOCK_MODELS.openai),
    chosen: 'none',
    via: await proxyModelOption(context, JUNIE_MODEL_WITHOUT_NONE),
    viaEfforts: ['max', 'xhigh', 'high', 'medium', 'low'],
    settled: 'low',
    nativeProof: request => expectProxyRequest(request, MOCK_MODELS.openai, 'low'),
  })
})

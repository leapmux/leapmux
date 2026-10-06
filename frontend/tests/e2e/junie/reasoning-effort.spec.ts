import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_PROXY_PROVIDER } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, waitForSettingsHydrated } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

/** Open the model picker of the composer and return its visible popover. */
async function openModelPicker(page: Page) {
  await page.locator('[data-testid="composer-model-trigger"]:visible').click()
  return page.locator('[data-testid="composer-model-popover"]:visible')
}

/** Require the request of the Responses route of the proxy model at the low effort. */
function expectLowEffortRequest(request: MockModelRequestRecord): void {
  expect(request.path).toBe('/v1/responses')
  expect(request.body).toMatchObject({ model: JUNIE_NATIVE_EFFORT_MODEL, reasoning: { effort: 'low' } })
}

junieTest.describe('Junie settings', () => {
  junieTest('an effort switch reaches the next request and survives a reload', async ({ authenticatedNativeEffortJunieWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedNativeEffortJunieWorkspace.workspaceId })
    let proxyModelOptionID = ''
    let answeredTurns = 0
    await exerciseNativeOption(context, {
      groupId: 'effort',
      value: 'low',
      prepare: async () => {
        await waitForSettingsHydrated(page)
        const proxyModel = (await openModelPicker(page)).getByRole('option', { name: 'GPT-5.3-codex', exact: true })
        await expect(proxyModel).toHaveCount(1)
        await expect(proxyModel).toHaveAttribute('aria-selected', 'true')
        proxyModelOptionID = await proxyModel.getAttribute('data-testid') ?? ''
        if (!proxyModelOptionID)
          throw new Error('Junie did not identify the native proxy model option')
        expect(proxyModelOptionID).toContain(`proxy:${JUNIE_PROXY_PROVIDER}:${JUNIE_NATIVE_EFFORT_MODEL}`)
        await closeComposerMenus(page)
        await modelScript.rule(
          { name: 'junie-effort-capability-filter', when: { system: 'classify a user request and route it to the correct handler' }, respond: { text: 'CODE' } },
          { name: 'junie-effort-language', when: { system: 'You are a language identification utility.' }, respond: { text: JSON.stringify({ iso: 'en', confidence: 1 }) } },
          { name: 'junie-effort-next-prompt', when: { user: 'Return ONLY the predicted prompt \\(max 7 tokens\\)' }, respond: { text: 'NONE' } },
        )
      },
      nativeProof: async (request) => {
        expectLowEffortRequest(request)
        answeredTurns++
        // Junie predicts the next prompt after its first answered turn. The restore step reloads the page next, so
        // the proof waits for that one prediction first, and the reload cannot interrupt it.
        if (answeredTurns === 1)
          await expect.poll(async () => (await modelScript.status()).ruleMatches['junie-effort-next-prompt'] ?? 0).toBe(1)
      },
    })
    // The restore keeps the proxy model, not only the effort.
    await waitForSettingsHydrated(page)
    await expect((await openModelPicker(page)).getByTestId(proxyModelOptionID)).toHaveAttribute('aria-selected', 'true')
    await closeComposerMenus(page)
  })
})

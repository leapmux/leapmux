import { JUNIE_NATIVE_EFFORT_MODEL, JUNIE_PROXY_PROVIDER } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, closeComposerMenus, expectSettingsOptionChosen, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest.describe('Junie settings', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('an effort switch reaches the next request and survives a reload', async ({ authenticatedNativeEffortJunieWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedNativeEffortJunieWorkspace
    await waitForSettingsHydrated(page)
    const effortMenu = await openSettingsMenu(page, 'effort')
    await expect(effortMenu.getByTestId('effort-low')).toBeVisible()
    await closeComposerMenus(page)
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, 'effort-low')

    await page.locator('[data-testid="composer-model-trigger"]:visible').click()
    const modelMenu = page.locator('[data-testid="composer-model-popover"]:visible')
    const proxyModel = modelMenu.getByRole('option', { name: 'GPT-5.3-codex', exact: true })
    await expect(proxyModel).toHaveCount(1)
    await expect(proxyModel).toHaveAttribute('aria-selected', 'true')
    const proxyModelOptionID = await proxyModel.getAttribute('data-testid')
    if (!proxyModelOptionID)
      throw new Error('Junie did not identify the native proxy model option')
    expect(proxyModelOptionID).toContain(`proxy:${JUNIE_PROXY_PROVIDER}:${JUNIE_NATIVE_EFFORT_MODEL}`)
    await closeComposerMenus(page)

    await modelScript.rule(
      { name: 'junie-effort-capability-filter', when: { system: 'classify a user request and route it to the correct handler' }, respond: { text: 'CODE' } },
      { name: 'junie-effort-language', when: { system: 'You are a language identification utility.' }, respond: { text: JSON.stringify({ iso: 'en', confidence: 1 }) } },
      { name: 'junie-effort-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Effort switch task' } },
      { name: 'junie-effort-next-prompt', when: { user: 'Return ONLY the predicted prompt \\(max 7 tokens\\)' }, respond: { text: 'NONE' } },
    )
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-effort-answer', 'The low effort model answered.')] })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.path).toBe('/v1/responses')
    expect(request?.body).toMatchObject({ model: JUNIE_NATIVE_EFFORT_MODEL, reasoning: { effort: 'low' } })

    await expect.poll(async () => (await modelScript.status()).ruleMatches['junie-effort-next-prompt'] ?? 0).toBe(1)

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedNativeEffortJunieWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'effort',
      value: 'low',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: JUNIE_NATIVE_EFFORT_MODEL, reasoning: { effort: 'low' } })
        expect(request.path).toBe('/v1/responses')
      },
    })
    await waitForSettingsHydrated(page)
    await page.locator('[data-testid="composer-model-trigger"]:visible').click()
    await expect(page.locator('[data-testid="composer-model-popover"]:visible').getByTestId(proxyModelOptionID)).toHaveAttribute('aria-selected', 'true')
    await closeComposerMenus(page)
    await expectSettingsOptionChosen(page, 'effort-low')
  })
})

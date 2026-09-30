import { JUNIE_MOCK_MODEL, JUNIE_NATIVE_EFFORT_MODEL, JUNIE_PROXY_PROVIDER, JUNIE_RESPONSES_MODEL } from './helpers/mockAgentEnvironment'
import { junieAnswerToolCall } from './helpers/providerToolCalls'
import {
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  expectSettingsOptionChosen,
  openPlusMenu,
  openSettingsMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

junieTest.describe('Junie settings', () => {
  // Junie's mode axis is its `mode` config option: Default and Plan. A new
  // session runs Default, so the mode menu shows Default checked and Plan as
  // the other choice. The model comes from the custom-model profile the
  // environment writes, and the effort axis is the well-known `effort` id.
  junieTest('the settings menu offers the model, effort, and mode axes', async ({ authenticatedJunieWorkspace, page }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)

    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator('[data-testid="permissionMode-default"] input[type="radio"]')).toBeChecked()
    await expect(mode.locator('[data-testid="permissionMode-plan"] input[type="radio"]')).toBeVisible()
    await closeComposerMenus(page)

    // Junie's `effort` config option takes low, medium or high.
    const effort = await openSettingsMenu(page, 'effort')
    await expect(effort.locator('[data-testid="effort-low"]')).toBeVisible()
    await expect(effort.locator('[data-testid="effort-medium"]')).toBeVisible()
    await expect(effort.locator('[data-testid="effort-high"]')).toBeVisible()
    await closeComposerMenus(page)

    // The pinned custom-model profile is the only model the session lists.
    const model = await openSettingsMenu(page, 'model')
    await expect(model.locator(`[data-testid="model-${JUNIE_MOCK_MODEL}"]`).first()).toBeVisible()
    await closeComposerMenus(page)
  })

  // Plan mode is a mode config option for Junie (matrix note 27), not a tool.
  // Choosing it writes the option live and the chip follows the selection.
  junieTest('a mode switch to Plan reaches the chip and survives a reload', async ({ authenticatedJunieWorkspace, page }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
  })

  junieTest('a model switch reaches the native Responses endpoint and survives a reload', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${JUNIE_RESPONSES_MODEL}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${JUNIE_RESPONSES_MODEL}`)

    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Model switch task' } },
    )
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-model-answer', 'The selected model answered.')] })
    await sendMessage(page, modelScript.prompt('Reply once with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(status.requests.find(request => request.stepIndex === 0)?.path).toBe('/v1/responses')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `model-${JUNIE_RESPONSES_MODEL}`)
  })

  junieTest('an effort switch reaches the next request and survives a reload', async ({ authenticatedNativeEffortJunieWorkspace, page, modelScript }) => {
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

    await page.reload()
    await waitForSettingsHydrated(page)
    await page.locator('[data-testid="composer-model-trigger"]:visible').click()
    await expect(page.locator('[data-testid="composer-model-popover"]:visible').getByTestId(proxyModelOptionID)).toHaveAttribute('aria-selected', 'true')
    await closeComposerMenus(page)
    await expectSettingsOptionChosen(page, 'effort-low')
    await expect.poll(async () => (await modelScript.status()).ruleMatches['junie-effort-next-prompt'] ?? 0).toBe(1)
  })
})

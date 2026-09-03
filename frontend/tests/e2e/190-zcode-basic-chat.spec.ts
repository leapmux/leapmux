import { ARITHMETIC_PROMPT, assistantBubbles, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, messageContents, openPlusMenu, openSettingsMenu, sendMessage, settingsBar, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest.describe('ZCode Basic Chat', () => {
  zcodeTest('opens, sends a prompt, and receives a response', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
  })

  zcodeTest('assistant response appears in a chat bubble', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await sendMessage(page, 'Say hello world')
    await waitForAgentIdle(page, 180_000)

    await expect(assistantBubbles(page)).not.toHaveCount(0)
    // expectAssistantAnswer, not lastAssistantBubble: a turn-end divider is an
    // agent-role bubble too, so the LAST one is the divider whenever it lands
    // after the reply.
    await expectAssistantAnswer(page, { answer: /hello/i })
  })
})

zcodeTest.describe('ZCode Tool Execution', () => {
  zcodeTest('a bash command renders as a tool card with its output', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await sendMessage(page, 'Run the bash command: echo "zcode-test-output" and show me the output.')
    await waitForAgentIdle(page, 180_000)

    const joined = (await messageContents(page).allTextContents()).join(' ')
    expect(joined).toContain('zcode-test-output')
  })
})

zcodeTest.describe('ZCode Permission Prompt', () => {
  // Build is the default and asks before a risky action. A destructive command
  // is the shape that produces a permission banner rather than running silently.
  zcodeTest('a risky command produces a permission banner that can be denied', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await sendMessage(page, 'Run this exact bash command and do not skip the confirmation: rm -rf /tmp/zcode-e2e-must-not-exist')

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Bash')
    // Reject lives in the composer footer, not inside the banner slot --
    // ControlRequestContent and ControlRequestActions render in different
    // places, the same split every other provider's e2e already follows.
    const deny = page.getByTestId('control-deny-btn')
    await expect(deny).toBeVisible()
    await deny.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
  })
})

zcodeTest.describe('ZCode Mode Switch', () => {
  zcodeTest('offers only the bypass permission shortcut', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await waitForSettingsHydrated(page)
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await menu.getByTestId('composer-bypass-permissions').click()
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Yolo')
  })

  zcodeTest('the mode chip starts on Build and can switch to Plan and Yolo', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await expect(settingsBar(page)).toBeVisible()
    await expectSettingsChip(page, 'Build')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'permissionMode-yolo')
    await expectSettingsChip(page, 'Yolo')

    await chooseSettingsOption(page, 'permissionMode-build')
    await expectSettingsChip(page, 'Build')
  })

  zcodeTest('auto is not offered, because the shipped app-server does not implement it', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    const menu = await openSettingsMenu(page, 'permissionMode')
    await expect(menu.locator('[data-testid="permissionMode-auto"]')).toHaveCount(0)
    await expect(menu.locator('[data-testid="permissionMode-plan"]')).toBeVisible()
    await expect(menu.locator('[data-testid="permissionMode-build"]')).toBeVisible()
    await expect(menu.locator('[data-testid="permissionMode-edit"]')).toBeVisible()
    await expect(menu.locator('[data-testid="permissionMode-yolo"]')).toBeVisible()
    await page.keyboard.press('Escape')
  })
})

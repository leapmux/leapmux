import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect, expectCodewhalePosture } from './codewhale-fixtures'
import {
  applyPermissionPreset,
  chooseSettingsOption,
  expectSettingsChip,
  openPlusMenu,
  sendMessage,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('applies the mode, the effort and the posture, and keeps them after a reload', async ({ authenticatedCodewhaleWorkspace, page }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')
    await expectCodewhalePosture(page, 'ask')

    await chooseSettingsOption(page, 'codewhale_mode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    // Both presets map onto a posture of the runtime: Smart onto its own review
    // rules, Bypass onto full access.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    await expectCodewhalePosture(page, 'full_access')
    await applyPermissionPreset(page, 'smart')
    await expectCodewhalePosture(page, 'auto_review')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'High')
    await expectCodewhalePosture(page, 'auto_review')

    await chooseSettingsOption(page, 'codewhale_mode-agent')
    await chooseSettingsOption(page, 'permissionMode-ask')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')
    await expectCodewhalePosture(page, 'ask')
  })

  codewhaleTest('Shift+Tab toggles plan mode from the composer', async ({ authenticatedCodewhaleWorkspace, page }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')
  })

  codewhaleTest('sends the chosen effort with the next turn', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)

    // The runtime takes the effort per turn, so the model request is where the
    // choice must arrive.
    await modelScript.queue({ text: 'Done.' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: Done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const { requests } = await modelScript.status()
    expect(JSON.stringify(requests[0]!.body)).toContain('"reasoning_effort":"high"')
  })
})

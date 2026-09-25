import {
  applyPermissionPreset,
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  chooseSettingsOption,
  expectAssistantAnswer,
  expectSettingsChip,
  openPlusMenu,
  sendMessage,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/**
 * 138 — Oh My Pi settings.
 *
 * omp applies a thinking level live (`set_thinking_level`), and an approval mode only
 * at launch (`--approval-mode`), so a change of the approval mode restarts the
 * agent. Both must survive that restart and a reload, and the thinking level must
 * reach the model.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('applies Oh My Pi settings, keeps them over a restart and a reload, and sends the thinking level', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  await waitForSettingsHydrated(page)
  // The fixture opens the agent in omp's Yolo mode.
  await expectSettingsChip(page, 'Yolo')

  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')

  // A new approval mode restarts omp with the new `--approval-mode`.
  await chooseSettingsOption(page, 'permissionMode-always-ask')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Always Ask')
  await expectSettingsChip(page, 'Low')

  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Always Ask')
  await expectSettingsChip(page, 'Low')

  // The thinking level reaches the model after the restart.
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  const request = status.requests.find(record => record.stepIndex === 0)
  expect((request?.body as { reasoning_effort?: unknown } | undefined)?.reasoning_effort).toBe('low')

  // omp has no smart mode. Bypass selects Yolo.
  const menu = await openPlusMenu(page)
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
  await page.keyboard.press('Escape')
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
  await expectSettingsChip(page, 'Low')
})

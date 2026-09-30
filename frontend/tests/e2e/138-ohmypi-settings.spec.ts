import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { OH_MY_PI_ALT_MODEL_ID, OH_MY_PI_ALT_MODEL_WIRE_ID } from './helpers/mockAgentEnvironment'
import { bashToolCall } from './helpers/providerToolCalls'
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
  waitForControlBanner,
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

ohMyPiTest('switches the model for the next native request', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, `model-${OH_MY_PI_ALT_MODEL_ID}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'GLM-5.3 Alternate')

  await modelScript.queue({ text: 'The alternate model answered.' })
  await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
  expect(body.includes(`"model":"${OH_MY_PI_ALT_MODEL_WIRE_ID}"`)).toBe(true)

  await page.reload()
  await expectSettingsChip(page, 'GLM-5.3 Alternate')
})

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
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  const request = status.requests.find(record => record.stepIndex === 0)
  expect((request?.body as { reasoning_effort?: unknown } | undefined)?.reasoning_effort).toBe('low')

  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'ask-mode-call', 'echo "omp-mode-$((40 + 2))"')] },
    { text: 'The Always Ask turn ended.' },
  )
  await sendMessage(page, modelScript.prompt('Run the scripted command in Always Ask mode.'))
  await modelScript.waitForSteps(2)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('omp-mode-')
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
  const askedStatus = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const askedMessages = (askedStatus.requests.find(record => record.stepIndex === 2)?.body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
  const askedResult = askedMessages.findLast(message => message.role === 'tool')?.content
  expect(JSON.stringify(askedResult)).toContain('omp-mode-42')

  // omp has no smart mode. Bypass selects Yolo.
  const menu = await openPlusMenu(page)
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
  await page.keyboard.press('Escape')
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
  await expectSettingsChip(page, 'Low')

  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'bypass-mode-call', 'echo "omp-bypass-$((50 + 5))"')] },
    { text: 'The Yolo turn ended.' },
  )
  await sendMessage(page, modelScript.prompt('Run the scripted command after Bypass.'))
  await modelScript.waitForSteps(4)
  await expect(banner).toHaveCount(0)
  const bypassStatus = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const bypassMessages = (bypassStatus.requests.find(record => record.stepIndex === 4)?.body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
  const bypassResult = bypassMessages.findLast(message => message.role === 'tool')?.content
  expect(JSON.stringify(bypassResult)).toContain('omp-bypass-55')
})

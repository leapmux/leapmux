import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { OH_MY_PI_ALT_MODEL_ID, OH_MY_PI_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The selected effort must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * `set_thinking_level` changes native effort live. The approval mode changes at launch, so its change restarts the agent.
 */
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

ohMyPiTest('sends low native effort before and after reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// omp keeps the thinking level between reasoning models, and LeapMux folds the level that omp reports.
ohMyPiTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: OH_MY_PI_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(JSON.stringify(request.body)).toContain(`"model":"${OH_MY_PI_ALT_MODEL_WIRE_ID}"`)
      expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    },
  })
})

import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { OH_MY_PI_ALT_MODEL_ID, OH_MY_PI_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectPermissionShortcuts, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The selected effort must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * `set_thinking_level` changes native effort live. The approval mode changes at launch, so its change restarts the agent.
 */
ohMyPiTest('applies Oh My Pi settings, keeps them over a restart and a reload, and sends the thinking level', async ({ native }) => {
  const { page, modelScript } = native
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
  const lowStep = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(lowStep + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  expect((await modelScript.requestAt(lowStep)).body).toHaveProperty('reasoning_effort', 'low')

  await exerciseNativePermissionDecision(native, {
    toolCall: bashToolCall(AgentProvider.OH_MY_PI, 'ask-mode-call', 'echo "omp-mode-$((40 + 2))"'),
    decision: 'allow',
    beforeDecision: banner => expect(banner).toContainText('omp-mode-'),
    nativeProof: asked => expect(nativeToolResult(asked, 'ask-mode-call')).toContain('omp-mode-42'),
  })

  // omp has no smart mode. Bypass selects Yolo.
  await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
  await expectSettingsChip(page, 'Low')

  // Yolo runs the command with no permission request, so the turn clicks nothing.
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'bypass-mode-call', 'echo "omp-bypass-$((50 + 5))"')],
    prompt: 'Run the scripted command after Bypass.',
    answer: 'The Yolo turn ended.',
    permissions: 'none',
  })
  expect(nativeToolResult(resultRequest, 'bypass-mode-call')).toContain('omp-bypass-55')
})

ohMyPiTest('sends low native effort before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// omp keeps the thinking level between reasoning models, and LeapMux folds the level that omp reports.
ohMyPiTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: OH_MY_PI_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(JSON.stringify(request.body)).toContain(`"model":"${OH_MY_PI_ALT_MODEL_WIRE_ID}"`)
      expect(request.body).toMatchObject({ reasoning_effort: 'low' })
    },
  })
})

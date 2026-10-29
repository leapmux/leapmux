import { existsSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

gooseTest('smart mode asks before a removal and auto mode runs it', async ({ native, authenticatedGooseWorkspace }) => {
  const { page, modelScript } = native
  const workingDir = authenticatedGooseWorkspace.workingDir
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(GOOSE_MODE))
  for (const phase of ['before-reload', 'after-reload']) {
    const marker = join(workingDir, `goose-mode-marker-${phase}.txt`)
    const removal = `rm -f ${basename(marker)} && printf 'goose-mode-%s' "$((40 + 2))"`
    writeFileSync(marker, 'keep this file\n')
    await chooseSettingsOption(page, `permissionMode-${GOOSE_MODE.SmartApprove}`)
    await waitForSettingsIdle(page)
    if (phase === 'after-reload')
      await page.reload()
    await expectSettingsOptionChosen(page, `permissionMode-${GOOSE_MODE.SmartApprove}`)
    expect(nativeOptionValue(await currentNativeAgent(native), 'permissionMode')).toBe(GOOSE_MODE.SmartApprove)
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(AgentProvider.GOOSE, `goose-smart-remove-${phase}`, removal),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(basename(marker)),
      nativeProof: () => expect(existsSync(marker)).toBe(true),
    })

    await applyPermissionPreset(page, 'bypass')
    await waitForSettingsIdle(page)
    if (phase === 'after-reload')
      await page.reload()
    await expectSettingsOptionChosen(page, `permissionMode-${GOOSE_MODE.Auto}`)
    expect(nativeOptionValue(await currentNativeAgent(native), 'permissionMode')).toBe(GOOSE_MODE.Auto)
    // Auto mode runs the removal without a banner, so the scenario answers no control.
    const autoCall = `goose-auto-remove-${phase}`
    const autoStep = await modelScript.queue(
      {
        toolCalls: [
          bashToolCall(AgentProvider.GOOSE, autoCall, removal),
        ],
      },
      { text: 'The Auto check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Auto.'))
    await modelScript.waitForSteps(autoStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(marker)).toBe(false)
    expect(await nativeToolResultAt(modelScript, autoStep + 1, autoCall)).toContain('goose-mode-42')
  }
})

gooseTest('returns from Auto to Approve with one notice and restored native approval', async ({ native, authenticatedGooseWorkspace }) => {
  const { page, modelScript } = native
  await waitForNativeSettingsHydrated(page)
  const autoNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: GOOSE_MODE.Auto,
    label: 'Auto',
    previousLabel: 'Smart Approve',
    noticeLabel: 'Mode',
  })
  const marker = join(authenticatedGooseWorkspace.workingDir, 'goose-return-mode-marker.txt')
  writeFileSync(marker, 'The native removal must run.\n')
  const command = `rm -f ${basename(marker)} && printf 'goose-return-%s' "$((40 + 2))"`
  const step = await modelScript.queue(
    {
      toolCalls: [
        bashToolCall(AgentProvider.GOOSE, 'goose-return-auto', command),
      ],
    },
    { text: 'The Auto removal ran.' },
  )
  await sendMessage(page, modelScript.prompt('Run the scripted removal in Auto mode.'))
  await modelScript.waitForSteps(step + 2)
  await waitForAgentIdle(page)
  await expectNoControlBanner(page)
  expect(existsSync(marker)).toBe(false)
  expect(await nativeToolResultAt(modelScript, step + 1, 'goose-return-auto')).toContain('goose-return-42')

  const approveNotice = await chooseNativeModeWithNotice(native, {
    groupId: 'permissionMode',
    value: GOOSE_MODE.Approve,
    label: 'Approve',
    previousLabel: 'Auto',
    noticeLabel: 'Mode',
  })
  const classifier = 'goose-return-mode-safe-classifier'
  await modelScript.rule({
    name: classifier,
    when: { system: 'permission-safety classifier' },
    respond: {
      toolCalls: [
        goosePermissionJudgmentToolCall('goose-return-mode-judge', ['goose-approve-safe-returned', 'goose-approve-safe-restored']),
      ],
    },
  })
  for (const phase of ['returned', 'restored']) {
    if (phase === 'restored') {
      await page.reload()
      await waitForNativeSettingsHydrated(page)
    }
    await expectSettingsChip(page, 'Approve')
    writeFileSync(marker, 'The native approval must ask.\n')
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(AgentProvider.GOOSE, `goose-return-approve-${phase}`, command),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(basename(marker)),
      nativeProof: () => expect(existsSync(marker)).toBe(true),
    })
    const safeCall = `goose-approve-safe-${phase}`
    const safeCommand = 'printf "goose-approve-safe-%s" "$((40 + 2))"'
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(AgentProvider.GOOSE, safeCall, safeCommand),
      decision: 'allow',
      beforeDecision: banner => expect(banner).toContainText('goose-approve-safe-'),
      nativeProof: request => expect(nativeToolResult(request, safeCall)).toContain('goose-approve-safe-42'),
    })
  }
  await autoNotice.assertStored()
  await approveNotice.assertStored()
})

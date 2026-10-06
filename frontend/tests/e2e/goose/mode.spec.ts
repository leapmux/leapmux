import { existsSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

gooseTest('smart mode asks before a removal and auto mode runs it', async ({ native, authenticatedGooseWorkspace }) => {
  const { page, modelScript } = native
  const workingDir = authenticatedGooseWorkspace.workingDir
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
      { toolCalls: [bashToolCall(AgentProvider.GOOSE, autoCall, removal)] },
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

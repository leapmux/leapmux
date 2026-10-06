import { existsSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

gooseTest('smart mode asks before a removal and auto mode runs it', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = authenticatedGooseWorkspace.workingDir
  if (!workingDir)
    throw new Error('The Goose workspace has no working directory.')
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  for (const phase of ['before-reload', 'after-reload']) {
    const marker = join(workingDir, `goose-mode-marker-${phase}.txt`)
    writeFileSync(marker, 'keep this file\n')
    await chooseSettingsOption(page, `permissionMode-${GOOSE_MODE.SmartApprove}`)
    await waitForSettingsIdle(page)
    if (phase === 'after-reload')
      await page.reload()
    await expectSettingsOptionChosen(page, `permissionMode-${GOOSE_MODE.SmartApprove}`)
    expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe(GOOSE_MODE.SmartApprove)
    const start = (await modelScript.status()).stepCount
    const smartCall = `goose-smart-remove-${phase}`
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GOOSE, smartCall, `rm -f ${basename(marker)} && printf 'goose-mode-%s' "$((40 + 2))"`)] },
      { text: 'The Smart check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted removal under Smart Approve.'))
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(basename(marker))
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(existsSync(marker)).toBe(true)
    await applyPermissionPreset(page, 'bypass')
    await waitForSettingsIdle(page)
    if (phase === 'after-reload')
      await page.reload()
    await expectSettingsOptionChosen(page, `permissionMode-${GOOSE_MODE.Auto}`)
    expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe(GOOSE_MODE.Auto)
    const autoCall = `goose-auto-remove-${phase}`
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GOOSE, autoCall, `rm -f ${basename(marker)} && printf 'goose-mode-%s' "$((40 + 2))"`)] },
      { text: 'The Auto check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Auto.'))
    const status = await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(marker)).toBe(false)
    const result = nativeToolResult(status.requests.find(request => request.stepIndex === start + 3), autoCall)
    expect(result).toContain('goose-mode-42')
  }
})

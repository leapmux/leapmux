import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

grokTest('keeps the native Plan constraint after the setting changes and reloads', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectSettingsOptionChosen(page, 'permissionMode-plan')
    const path = join(agent.workingDir, reload ? 'restored-plan-write.txt' : 'native-plan-write.txt')
    expect(existsSync(path)).toBe(false)
    const callId = `denied-plan-write-${reload ? 'restored' : 'selected'}`
    const start = await modelScript.queue(
      { toolCalls: [writeToolCall(native.provider, callId, { path, content: 'This Plan write must not run.\n' })] },
      { text: 'The native Plan write was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Attempt the scripted file write under the current setting.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(existsSync(path)).toBe(false)
    expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toMatch(/plan|refus|denied|not allowed/i)
  }
})

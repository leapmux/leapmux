import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolRowId } from '../helpers/nativeToolRowId'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, interruptButton, messageContents, sendMessage, toolCallRow, toolRows, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('renders Kimi Code live tool output', () => {
  for (const finish of ['completion', 'interruption'] as const) {
    kimiTest(`keeps live command output after ${finish} and reload`, async ({ native }) => {
      const { page, modelScript } = native
      await waitForSettingsHydrated(page)
      await applyPermissionPreset(page, 'bypass')
      const agent = await currentNativeAgent(native)
      const output = createToolOutputControl(agent.workingDir)
      await withCleanup(async () => {
        const start = await modelScript.queue({
          toolCalls: [
            bashToolCall(native.provider, 'live-output', output.command),
          ],
        }, nativeTextStep(native, 'The controlled command finished.'))
        await sendMessage(page, modelScript.prompt('Run the controlled command and report when it ends.'))
        await modelScript.waitForSteps(start + 1)
        await output.waitForFirstOutput()
        await expect(messageContents(page).filter({ hasText: output.firstLiveTail }).first()).toBeVisible()
        await output.releaseFirstOutput()
        await output.waitForSecondOutput()
        await expect(messageContents(page).filter({ hasText: output.secondLiveTail }).first()).toBeVisible()
        if (finish === 'interruption') {
          modelScript.allowUnconsumed('The interruption ends the tool turn before the scripted final answer.')
          await interruptButton(page).click()
        }
        else {
          await output.releaseFinalOutput()
          await modelScript.waitForSteps(start + 2)
        }
        await waitForAgentIdle(page)
        if (finish === 'interruption')
          expect((await modelScript.status()).nextStep).toBe(start + 1)
        const rowId = await nativeToolRowId(native, 'live-output')
        for (const reload of [false, true]) {
          if (reload) {
            await page.reload()
            await waitForAgentIdle(page)
          }
          const rows = toolRows(page)
          await expect(rows.filter({ hasText: output.secondMarker }).first()).toBeVisible()
          await expandNativeResultView(toolCallRow(page, rowId))
          await expect(rows.filter({ hasText: output.secondLiveTail }).first()).toBeVisible()
          if (finish === 'completion')
            await expect(rows.filter({ hasText: output.firstMarker }).first()).toBeVisible()
        }
      }, async () => {
        await output.releaseFirstOutput()
        await output.releaseFinalOutput()
      })
    })
  }
})

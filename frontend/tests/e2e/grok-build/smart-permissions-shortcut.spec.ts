import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

grokTest('runs routine work and blocks a risky native command under Smart before and after reload', async ({ native }) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const file = join(agent.workingDir, 'native-smart-routine.txt')
  await applyPermissionPreset(page, 'smart')
  await modelScript.rule({
    name: 'native-grok-smart-block',
    when: { system: '^You review a command that a coding agent wants to run' },
    respond: { text: '{"thinking":"The command removes the fixture file.","shouldBlock":true,"reason":"Ask before the fixture removal."}' },
  })
  const routine = await modelScript.queue({ toolCalls: [bashToolCall(native.provider, 'native-smart-routine', `touch ${quotePosixShellArgument(file)}`)] }, nativeTextStep(native, 'The routine native command ran.'))
  await sendMessage(page, modelScript.prompt('Run the scripted routine command under Smart permissions.'))
  await modelScript.waitForSteps(routine + 2)
  await waitForAgentIdle(page)
  expect(existsSync(file)).toBe(true)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectNoNativeControl(native, { testId: 'control-banner', relatedProof: async () => {
      const reviewedBefore = (await modelScript.status()).ruleMatches['native-grok-smart-block'] ?? 0
      // One agent session runs both passes, so each pass gives its tool call its own ID.
      const id = `native-smart-block-${Number(reload)}`
      const start = await modelScript.queue({ toolCalls: [bashToolCall(native.provider, id, `rm -rf ${quotePosixShellArgument(file)}`)] }, nativeTextStep(native, 'The native Smart reviewer blocked the removal.'))
      await sendMessage(page, modelScript.prompt('Attempt the scripted risky removal under Smart permissions.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(existsSync(file)).toBe(true)
      expect(nativeToolResult(await modelScript.requestAt(start + 1), id)).toContain('Auto mode blocked this action')
      expect(status.ruleMatches['native-grok-smart-block']).toBeGreaterThan(reviewedBefore)
    } })
  }
})

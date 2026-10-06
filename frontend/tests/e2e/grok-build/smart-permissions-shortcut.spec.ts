import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

grokTest('runs routine work and blocks a risky native command under Smart before and after reload', async ({ authenticatedGrokWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  const agent = await currentNativeAgent(context)
  const file = join(agent.workingDir, 'native-smart-routine.txt')
  await applyPermissionPreset(page, 'smart')
  await modelScript.rule({
    name: 'native-grok-smart-block',
    when: { system: '^You review a command that a coding agent wants to run' },
    respond: { text: '{"thinking":"The command removes the fixture file.","shouldBlock":true,"reason":"Ask before the fixture removal."}' },
  })
  await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, 'native-smart-routine', `touch ${quotePosixShellArgument(file)}`)] }, { text: 'The routine native command ran.' })
  await sendMessage(page, modelScript.prompt('Run the scripted routine command under Smart permissions.'))
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(existsSync(file)).toBe(true)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectNoNativeControl(context, { testId: 'control-banner', relatedControl: async () => {
      const start = (await modelScript.status()).stepCount
      const reviewedBefore = (await modelScript.status()).ruleMatches['native-grok-smart-block'] ?? 0
      const id = `native-smart-block-${start}`
      await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.GROK_BUILD, id, `rm -rf ${quotePosixShellArgument(file)}`)] }, { text: 'The native Smart reviewer blocked the removal.' })
      await sendMessage(page, modelScript.prompt('Attempt the scripted risky removal under Smart permissions.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(existsSync(file)).toBe(true)
      expect(nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), id)).toContain('Auto mode blocked this action')
      expect(status.ruleMatches['native-grok-smart-block']).toBeGreaterThan(reviewedBefore)
    } })
  }
})

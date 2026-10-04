import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest('uses the native Smart reviewer and restores its setting after reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const agent = await currentNativeAgent(context)
  const file = join(agent.workingDir, '.env')
  expect(existsSync(file)).toBe(false)
  await modelScript.rule({
    name: 'native-codewhale-smart-review',
    when: { system: '^You are the Auto-Review guardian for a coding agent' },
    respond: { text: '{"risk_level":"low","decision":"allow","reason":"The isolated fixture write is reversible."}' },
  })
  await applyPermissionPreset(page, 'smart')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    await expectNoNativeControl(context, { testId: 'control-banner', relatedControl: async () => {
      const start = (await modelScript.status()).stepCount
      const content = reload ? 'NATIVE_SMART_RESTORED=42\n' : 'NATIVE_SMART_APPLIED=42\n'
      const id = `native-smart-review-${start}`
      await modelScript.queue(
        { toolCalls: [writeToolCall(AgentProvider.CODEWHALE, id, { path: file, content })] },
        { text: 'The native reviewer permitted the fixture write.' },
      )
      await sendMessage(page, modelScript.prompt('Run the scripted protected fixture write under Smart permissions.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(readFileSync(file, 'utf8')).toBe(content)
      expect(nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), id)).not.toMatch(/denied|not allowed/i)
      expect(status.ruleMatches['native-codewhale-smart-review']).toBeGreaterThan(0)
    } })
  }
})

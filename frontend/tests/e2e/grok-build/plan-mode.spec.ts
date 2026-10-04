import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { writeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('keeps the native Plan constraint after the setting changes and reloads', async ({ authenticatedGrokWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  const agent = await currentNativeAgent(context)
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
    const start = (await modelScript.status()).stepCount
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.GROK_BUILD, `denied-plan-write-${start}`, { path, content: 'This Plan write must not run.\n' })] },
      { text: 'The native Plan write was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Attempt the scripted file write under the current setting.'))
    const status = await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(existsSync(path)).toBe(false)
    const request = status.requests.find(record => record.stepIndex === start + 1)
    const body = request?.body as { messages?: { role?: string, content?: unknown }[] } | undefined
    const results = body?.messages?.filter(message => message.role === 'tool') ?? []
    expect(results.length).toBeGreaterThan(0)
    expect(JSON.stringify(results.at(-1)?.content)).toMatch(/plan|refus|denied|not allowed/i)
  }
})

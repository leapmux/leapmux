import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('requires real native review for protected writes under Smart before and after reload', async ({ authenticatedQwenWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  const agent = await currentNativeAgent(context)
  const file = join(agent.workingDir, 'package.json')
  await applyPermissionPreset(page, 'smart')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    const id = `native-smart-write-${reload}`
    await exerciseNativePermissionDecision(context, {
      toolCall: writeToolCall(AgentProvider.QWEN_CODE, id, { path: file, content: '{"private":true}\n' }),
      decision: 'deny',
      beforeDecision: async (banner) => {
        expect(existsSync(file)).toBe(false)
        await expect(banner).toContainText('package.json')
      },
      nativeProof: (request) => {
        expect(existsSync(file)).toBe(false)
        expect(nativeToolResult(request, id)).toMatch(/denied|reject|not allowed/i)
      },
    })
  }
})

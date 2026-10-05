import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { exerciseNativePermissionRefusal } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, waitForSettingsHydrated } from '../helpers/ui'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const CLASSIFIER_RULE = 'qwen-auto-classifier-states-no-verdict'

// Smart is Qwen's Auto mode. Before Auto mode runs a write to a protected file, it asks a classifier model.
// That request carries its own system prompt, so a rule answers it and the scripted turns keep their order.
// A classifier that states no verdict makes Qwen ask the reader ("Auto Mode couldn't classify this action").
// A reject then ends the turn: Qwen sends the model no tool result and sends no further request.
qwenTest('requires real native review for protected writes under Smart before and after reload', async ({ authenticatedQwenWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  const agent = await currentNativeAgent(context)
  const file = join(agent.workingDir, 'package.json')
  await applyPermissionPreset(page, 'smart')
  await modelScript.rule({
    name: CLASSIFIER_RULE,
    when: { system: '^You are a security classifier' },
    respond: { text: 'The classifier states no verdict.' },
  })
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    const id = `native-smart-write-${reload}`
    const toolCall = writeToolCall(AgentProvider.QWEN_CODE, id, { path: file, content: '{"private":true}\n' })
    const classifiedBefore = (await modelScript.status()).ruleMatches[CLASSIFIER_RULE] ?? 0
    await exerciseNativePermissionRefusal(context, {
      toolCall,
      prompt: 'Run the scripted permission probe.',
      bannerText: 'package.json',
      expectUnchanged: () => expect(existsSync(file)).toBe(false),
      nativeRefusal: (snapshot) => {
        const refusal = readNativeToolOutputRecord(snapshot, {
          callId: id,
          spanId: id,
          accepts: frame => frame.sessionUpdate === ACP_UPDATE.ToolCallUpdate && frame.toolCallId === id && frame.status === 'failed',
        })
        expect(refusal.frame.content).toEqual([{ type: 'content', content: { type: 'text', text: `Tool "${toolCall.name}" was canceled by the user.` } }])
      },
    })
    expect((await modelScript.status()).ruleMatches[CLASSIFIER_RULE], 'Auto mode asked its classifier before it asked the reader').toBeGreaterThan(classifiedBefore)
  }
})

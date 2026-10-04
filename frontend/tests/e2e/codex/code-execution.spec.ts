import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { codexTest } from '../codex-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution } from '../helpers/nativeCodeExecution'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEX }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  await exerciseNativeCodeExecution(context, { scripts: marker => [
    { label: 'output', source: `text(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
    { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
  ] })
})

codexTest('retains a completed native script with empty output after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId, createTestDirectory('native-code-empty-'), { agentProvider: AgentProvider.CODEX, ...agentOpenOptions(agentSettings(AgentProvider.CODEX)) })
  await openWorkspace(page, workspaceId)
  const callId = 'native-code-empty'
  await modelScript.queue({ toolCalls: [codexExecToolCall(callId, 'text("");')] }, { text: 'The empty native script completed.' })
  await sendMessage(page, modelScript.prompt('Run the empty native script.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const result = nativeToolResult(status.requests.find(request => request.stepIndex === 1), callId)
  expect(result).toContain('Script completed')
  expect(result).not.toContain('Script failed')
  const bubble = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-code-empty"][data-tool-row-role="result"]:visible')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, workspaceId)
    }
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
    await expect(assistantBubbles(page).filter({ hasText: 'The empty native script completed.' }).first()).toBeVisible()
  }
})

codexTest('retains native large-output truncation and its computed head and tail after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workspaceId = authenticatedEmptyWorkspace.workspaceId
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId, createTestDirectory('native-code-large-'), { agentProvider: AgentProvider.CODEX, ...agentOpenOptions(agentSettings(AgentProvider.CODEX)) })
  await openWorkspace(page, workspaceId)
  const callId = 'native-code-large'
  const source = '// @exec: {"max_output_tokens": 300}\ntext(Array.from({ length: 3000 }, (_, i) => "NATIVELARGE" + i + ":" + "x".repeat(40)).join("\\n"));'
  await modelScript.queue({ toolCalls: [codexExecToolCall(callId, source)] }, { text: 'The large native script completed.' })
  await sendMessage(page, modelScript.prompt('Run the large native script.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const result = nativeToolResult(status.requests.find(request => request.stepIndex === 1), callId)
  expect(result).toContain('Warning: truncated output')
  expect(result).toContain('NATIVELARGE0:')
  expect(result).toContain('NATIVELARGE2999:')
  expect(result).not.toContain('NATIVELARGE1500:')
  const bubble = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-code-large"][data-tool-row-role="result"]:visible')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, workspaceId)
    }
    await expect(bubble).toHaveCount(1)
    await expect(bubble).toHaveAttribute('data-tool-status', 'completed')
    await expect(bubble).toContainText('Warning: truncated output')
    await expandNativeResultView(bubble)
    await expect(bubble).toContainText('NATIVELARGE0:')
    await expect(bubble).toContainText('NATIVELARGE2999:')
    await expect(bubble).not.toContainText('NATIVELARGE1500:')
  }
})

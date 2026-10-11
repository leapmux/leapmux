import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution } from '../helpers/nativeCodeExecution'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codexTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })
  await exerciseNativeCodeExecution(context, { scripts: marker => [
    { label: 'output', source: `text(${JSON.stringify(marker)} + (40 + 2));`, expected: `${marker}42`, failed: false },
    { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
  ] })
})

codexTest('retains a completed native script with empty output after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const { workspaceId } = context
  await openNativeAgent(context, { directoryPrefix: 'native-code-empty-' })
  const callId = 'native-code-empty'
  const start = await modelScript.queue({ toolCalls: [codexExecToolCall(callId, 'text("");')] }, { text: 'The empty native script completed.' })
  await sendMessage(page, modelScript.prompt('Run the empty native script.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const result = nativeToolResult(await modelScript.requestAt(start + 1), callId)
  expect(result).toContain('Script completed')
  expect(result).not.toContain('Script failed')
  const bubble = toolCallRow(page, callId)
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
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const { workspaceId } = context
  await openNativeAgent(context, { directoryPrefix: 'native-code-large-' })
  const callId = 'native-code-large'
  const source = '// @exec: {"max_output_tokens": 300}\ntext(Array.from({ length: 3000 }, (_, i) => "NATIVELARGE" + i + ":" + "x".repeat(40)).join("\\n"));'
  const start = await modelScript.queue({ toolCalls: [codexExecToolCall(callId, source)] }, { text: 'The large native script completed.' })
  await sendMessage(page, modelScript.prompt('Run the large native script.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const result = nativeToolResult(await modelScript.requestAt(start + 1), callId)
  expect(result).toContain('Warning: truncated output')
  expect(result).toContain('NATIVELARGE0:')
  expect(result).toContain('NATIVELARGE2999:')
  expect(result).not.toContain('NATIVELARGE1500:')
  const bubble = toolCallRow(page, callId)
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

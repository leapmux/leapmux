import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'
import { readOpenCodeShellOutcome } from './nativeShellOutcome'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest('tool call renders with span', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace // fixture trigger

  // Script the native ls call so the case requires actual tool execution.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.OPENCODE, 'ls-call', 'ls')] },
    { text: 'The directory listing is above.' },
  )
  await sendMessage(page, modelScript.prompt('Use your shell tool to run `ls` in the current directory and report the output.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)

  // The native call must render a row with at least one span rail.
  // data-span-columns supplies that count. A row without a rail reports zero.
  // The former locator used two test IDs that no component emits and passed only when the spec was skipped.
  const railedRows = page.locator('[data-span-columns]:not([data-span-columns="0"]):visible')
  await expect(railedRows.first()).toBeVisible()
})

opencodeTest('keeps actual native shell output and a failed command result', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseShellToolExecution({ ...context, readToolResult: (request, callId) => readOpenCodeShellOutcome(context, request, callId) })
})

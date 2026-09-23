import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Tool Execution', () => {
  opencodeTest('tool call renders with span', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Force the agent to use a tool — listing files requires running `ls`,
    // which the agent must dispatch as a tool call. A response that doesn't
    // use a tool is a regression in this scenario.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OPENCODE, 'ls-call', 'ls')] },
      { text: 'The directory listing is above.' },
    )
    await sendMessage(page, modelScript.prompt('Use your shell tool to run `ls` in the current directory and report the output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    // A successful tool dispatch renders at least one row that draws a span
    // rail. If the agent answers without ever invoking a tool, that is the
    // regression this test was added to catch.
    //
    // data-span-columns is the count of rails on one row; a row that draws none
    // reports "0". The previous locator here specified two data-testids that no
    // component has ever emitted, so it could only pass while the whole spec
    // was skipped.
    const railedRows = page.locator('[data-span-columns]:not([data-span-columns="0"]):visible')
    await expect(railedRows.first()).toBeVisible()
  })
})

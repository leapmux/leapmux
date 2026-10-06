import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { cssAttributeValue } from '../helpers/cssAttribute'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code tool execution', () => {
  codebuddyTest('runs a Bash tool and draws its span', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    const call = bashToolCall(AgentProvider.CODEBUDDY, 'call-1', 'echo hi')
    const start = await modelScript.queue({ toolCalls: [call] }, { text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run echo hi.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    await expect(assistantBubbles(page).filter({ hasText: 'The command ran.' }).first()).toBeVisible()
    // A tool call opens a span, and each row of the span draws its rail. data-span-columns states how many rails a
    // row draws, and a row without a rail states zero. So a row of this call must state a nonzero count.
    const railedRows = page.locator('[data-span-columns]:not([data-span-columns="0"]):visible')
    await expect(railedRows.filter({ has: page.locator(`[data-tool-call-id="${cssAttributeValue(call.id)}"]`) }).first()).toBeVisible()
  })
})

codebuddyTest('runs successful and failed native commands with their actual output', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})

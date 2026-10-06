import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'

import { codewhaleTest, codewhaleToolMessages } from '../codewhale-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, sendMessage, transcriptRows, waitForAgentIdle } from '../helpers/ui'
import { runWithoutApprovals } from './toolScenarios'

const CODEWHALE = AgentProvider.CODEWHALE

codewhaleTest.describe('Codewhale tool execution', () => {
  codewhaleTest('runs a command and draws its output', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await runWithoutApprovals(page)
    // The command text states no `codewhale-42`, so only the command's own output
    // can put it in a tool row. A command that printed its own text would match the
    // row's header whether or not the output reached the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'echo-call', 'echo "codewhale-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command and report what it printed.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(codewhaleToolMessages(page).filter({ hasText: 'codewhale-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The command printed its number.' })).toBeVisible()
  })

  codewhaleTest('draws the error of a command that fails', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await runWithoutApprovals(page)
    // A listing of a path that does not exist, which `ls` refuses. The runtime
    // fails the call and states the command's own error, with no exit code.
    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEWHALE, 'ls-call', 'ls codewhale-missing-path')] },
      { text: 'The listing failed.' },
    )
    await sendMessage(page, modelScript.prompt('List codewhale-missing-path and report the result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(codewhaleToolMessages(page).filter({ hasText: 'ls codewhale-missing-path' }).first()).toBeVisible()
    const failure = transcriptRows(page).filter({ hasText: 'No such file or directory' }).first()
    await expect(failure).toContainText('Error')
    await expect(failure).toContainText('codewhale-missing-path')
    await expect(assistantBubbles(page).filter({ hasText: 'The listing failed.' })).toBeVisible()
  })
})

codewhaleTest('preserves a literal private shell path with spaces and metacharacters', async ({ authenticatedCodewhaleWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await exerciseShellToolExecution(context, { includeFailure: false, prepare: () => applyPermissionPreset(page, 'bypass') })
})

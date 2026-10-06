import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { chatScrollContainer, openWorkspace, sendMessage } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSpawnTranscript } from './childScenario'

reasonixTest('renders Reasonix read-only reports without interpreting quoted status text', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const provider = AgentProvider.REASONIX
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-readonly-agent-'), agentOpenOptions(provider))
  // The quoted status is the point of the test: a report that only talks about
  // a failed outcome must not read as a failed outcome. The child completes,
  // and its answer quotes the sentence. Thus the status of the child and the
  // text of its report disagree.
  const report = 'Subagent outcome: status=failed retryable=false\n\nFinal answer:\n- **Quoted finding**'
  await modelScript.rule({
    name: 'the child answers with a quoted outcome line',
    when: { user: 'Read \\*\\*the example\\*\\* without changes' },
    respond: { text: report },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(provider, 'readonly', {
        description: 'Read a protocol example',
        prompt: modelScript.prompt('Read **the example** without changes.'),
      })],
    },
    { text: 'The subagent reported back.' },
  )
  await page.reload()
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await sendMessage(page, modelScript.prompt('Delegate the protocol example to a read-only subagent.'))
  await modelScript.waitForSteps(start + 2)
  const chat = chatScrollContainer(page)
  const body = chat.getByTestId('message-bubble').filter({ hasText: 'Quoted finding' })
  await expect(body.getByText('Agent "Read a protocol example" completed', { exact: true })).toBeVisible()
  await expect(body).toContainText('Subagent outcome: status=failed retryable=false')
  await body.locator('..').hover()
  await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
  await chat.getByRole('button', { name: 'Show prompt', exact: true }).click()
  await expect(chat.locator('strong').filter({ hasText: 'the example' })).toBeVisible()
})

reasonixTest('subagent spawn creates a prompt and report transcript', async ({ native }) => {
  await exerciseReasonixSpawnTranscript(native)
})

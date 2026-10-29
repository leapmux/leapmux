import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { agentTabs, answerControl, assistantBubbles, controlButton, expectAgentTabCount, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'

const WORKFLOW_NAME = 'leapmux-e2e-probe'

const CHILD_RESULT = 'WORKFLOW_CHILD_OK'
const CHILD_RESULT_TWO = 'WORKFLOW_CHILD_TWO_OK'

function workflowScript(agents: readonly { prompt: string, label: string }[]): string {
  return [
    `export const meta = { name: ${JSON.stringify(WORKFLOW_NAME)}, description: 'Run one local probe', phases: [{ title: 'Probe' }] };`,
    `const results = [${agents.map(agent => `await agent(${JSON.stringify(agent.prompt)}, { label: ${JSON.stringify(agent.label)}, phase: 'Probe' })`).join(', ')}];`,
    'return { results };',
  ].join('\n')
}

claudeTest('shows each agent of a native Claude Workflow run under the run group', async ({ authenticatedWorkspace, page, modelScript }) => {
  void authenticatedWorkspace
  const tabsBefore = await agentTabs(page).count()
  await modelScript.rule({
    name: 'the workflow child answers locally',
    // Claude puts system reminders before the child's harness text. The root's
    // Workflow result quotes the prompt but does not carry this harness text.
    when: {
      user: ['\\[Workflow harness — computed task\\]', CHILD_RESULT],
    },
    respond: { text: CHILD_RESULT },
  })
  // Claude can start more root turns when the background workflow reports its
  // result. Their count depends on when the workflow notification arrives.
  await modelScript.fallback({ text: 'Workflow notification noted.' })
  const start = await modelScript.queue(
    {
      toolCalls: [
        codeExecutionToolCall(AgentProvider.CLAUDE_CODE, 'workflow-probe', workflowScript([
          {
            prompt: modelScript.prompt(`Reply with ${CHILD_RESULT}.`),
            label: 'probe',
          },
        ])),
      ],
    },
    { text: 'The workflow finished.' },
  )
  await sendMessage(page, modelScript.prompt('Run the one-child workflow and report its result.'))
  await modelScript.waitForSteps(start + 1)
  const permission = await waitForControlBanner(page)
  await expect(permission).toContainText('Permission Required')
  await expect(permission).toContainText(WORKFLOW_NAME)
  // A label other than `Allow` states a remembered allow, so the exact label proves an answer for this request only.
  await expect(controlButton(page, 'allow')).toHaveText('Allow')
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  await expect(assistantBubbles(page).filter({ hasText: 'The workflow finished.' }).first()).toBeVisible()
  await expandBackgroundTasksSection(page)
  const workflowRows = backgroundTaskRows(page, { kind: 'workflow' })
  const runRow = workflowRows.filter({ hasText: 'Run one local probe' }).first()
  await expect(runRow).toBeVisible()
  await expectRowBecomesFinal(page, runRow)
  await expect(runRow).toHaveAttribute('data-status', 'succeeded')
  const agentRows = backgroundTaskRows(page, { kind: 'subagent' })
  await expect(agentRows).toHaveCount(1)
  const agentRow = agentRows.filter({ hasText: 'probe' }).first()
  await expect(agentRow).toBeVisible()
  await expectRowBecomesFinal(page, agentRow)
  await expect(agentRow).toHaveAttribute('data-status', 'succeeded')
  // The run's row and its agent's row share one group heading: the workflow name.
  await expectRowsInWorkflowGroup([runRow, agentRow], WORKFLOW_NAME)
  await expect(runRow).toHaveAttribute('data-child-agent-id', '')
  await expect(agentRow).toHaveAttribute('data-child-agent-id', '')
  expect((await modelScript.status()).ruleMatches['the workflow child answers locally']).toBe(1)
  await expect(workflowRows).toHaveCount(1)
  await expectAgentTabCount(page, tabsBefore)
})

claudeTest('shows two agents of one Workflow run as two rows in one group', async ({ authenticatedWorkspace, page, modelScript }) => {
  void authenticatedWorkspace
  const tabsBefore = await agentTabs(page).count()
  await modelScript.rule({
    name: 'the first workflow child answers locally',
    when: {
      user: ['\\[Workflow harness — computed task\\]', CHILD_RESULT],
    },
    respond: { text: CHILD_RESULT },
  })
  await modelScript.rule({
    name: 'the second workflow child answers locally',
    when: {
      user: ['\\[Workflow harness — computed task\\]', CHILD_RESULT_TWO],
    },
    respond: { text: CHILD_RESULT_TWO },
  })
  await modelScript.fallback({ text: 'Workflow notification noted.' })
  const start = await modelScript.queue(
    {
      toolCalls: [
        codeExecutionToolCall(AgentProvider.CLAUDE_CODE, 'workflow-probe', workflowScript([
          {
            prompt: modelScript.prompt(`Reply with ${CHILD_RESULT}.`),
            label: 'first',
          },
          {
            prompt: modelScript.prompt(`Reply with ${CHILD_RESULT_TWO}.`),
            label: 'second',
          },
        ])),
      ],
    },
    { text: 'The workflow finished.' },
  )
  await sendMessage(page, modelScript.prompt('Run the two-child workflow and report its result.'))
  await modelScript.waitForSteps(start + 1)
  const permission = await waitForControlBanner(page)
  await expect(permission).toContainText('Permission Required')
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  await expandBackgroundTasksSection(page)
  const runRow = backgroundTaskRows(page, { kind: 'workflow' }).filter({ hasText: 'Run one local probe' }).first()
  await expectRowBecomesFinal(page, runRow)
  await expect(runRow).toHaveAttribute('data-status', 'succeeded')
  const agentRows = backgroundTaskRows(page, { kind: 'subagent' })
  await expect(agentRows).toHaveCount(2)
  const first = agentRows.filter({ hasText: 'first' }).first()
  const second = agentRows.filter({ hasText: 'second' }).first()
  for (const row of [first, second]) {
    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'succeeded')
    await expect(row).toHaveAttribute('data-child-agent-id', '')
  }
  // One heading groups the run row and both agent rows.
  await expectRowsInWorkflowGroup([runRow, first, second], WORKFLOW_NAME)
  const status = await modelScript.status()
  expect(status.ruleMatches['the first workflow child answers locally']).toBe(1)
  expect(status.ruleMatches['the second workflow child answers locally']).toBe(1)
  await expectAgentTabCount(page, tabsBefore)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/520.

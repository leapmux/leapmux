import { expect } from '@playwright/test'
import { zcodeCreateWorkflowToolCall, zcodeWorkflowSkillToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { answerControl, controlButton, expectNoControlBanner, sendMessage, waitForControlBanner } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { zcodeTest } from '../zcode-fixtures'

const WORKFLOW_NAME = 'leapmux-e2e-flow'

// ZCode states a CreateWorkflow run as one background task, and its subagents only through the conversation
// subscription of its desktop protocol. The Worker reads them there and draws each subagent's task under the run.
zcodeTest('groups the subagents of a native CreateWorkflow run under the run', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.fallback({ text: 'Workflow notification noted.' })
  await modelScript.rule(
    {
      name: 'the alpha subagent answers',
      when: { user: 'ZFLOW_ALPHA' },
      respond: { text: 'alpha done' },
      once: true,
    },
    {
      name: 'the beta subagent answers',
      when: { user: 'ZFLOW_BETA' },
      respond: { text: 'beta done' },
      once: true,
    },
  )
  const alphaTask = JSON.stringify(modelScript.prompt('ZFLOW_ALPHA: reply with alpha done.'))
  const betaTask = JSON.stringify(modelScript.prompt('ZFLOW_BETA: reply with beta done.'))
  const script = `const [a, b] = await Promise.all([agent("alpha").ask(${alphaTask}), agent("beta").ask(${betaTask})]);\nreturn { alpha: a, beta: b };`
  const start = await modelScript.queue(
    {
      toolCalls: [
        zcodeWorkflowSkillToolCall('load-workflow-skill'),
      ],
    },
    {
      toolCalls: [
        zcodeCreateWorkflowToolCall('create-workflow', WORKFLOW_NAME, script),
      ],
    },
    { text: 'The workflow launched.' },
  )
  await sendMessage(page, modelScript.prompt('Use a workflow to compute two answers.'))
  await modelScript.waitForSteps(start + 2)

  const permission = await waitForControlBanner(page)
  await expect(permission).toContainText('CreateWorkflow')
  await expect(permission).toContainText(WORKFLOW_NAME)
  // A label other than `Allow` states a remembered allow, so the exact label proves an answer for this request only.
  await expect(controlButton(page, 'allow')).toHaveText('Allow')
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 3)
  await expectNoControlBanner(page)

  await expandBackgroundTasksSection(page)
  const run = backgroundTaskRows(page, { kind: 'workflow' }).filter({ hasText: WORKFLOW_NAME }).first()
  const alpha = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'alpha' }).first()
  const beta = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'beta' }).first()
  for (const row of [run, alpha, beta]) {
    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'succeeded')
  }
  await expectRowsInWorkflowGroup([run, alpha, beta], WORKFLOW_NAME)
  const status = await modelScript.status()
  expect(status.ruleMatches['the alpha subagent answers']).toBe(1)
  expect(status.ruleMatches['the beta subagent answers']).toBe(1)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/520.

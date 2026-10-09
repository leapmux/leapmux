/**
 * A native Muse workflow groups its child work rows under one workflow heading.
 *
 * The model starts one native workflow through Muse's workflow tool. The script calls
 * host.agent() for each word; the host reports the run and its two children inside
 * the workflow item's fold, and the Worker draws one grouped row for each.
 */
import { expect } from '@playwright/test'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { museTest } from '../muse-fixtures'

/** The run's name, which Muse titles the workflow row and its group with. */
const WORKFLOW_NAME = 'Native grouping'

/** One word task of a child run of the workflow, marked so its model turn finds the scenario. */
function wordTask(word: string): string {
  return `Reply with the single word ${word}.`
}

/** The workflow script: two child agent calls and their words as the run's result. */
function workflowScript(first: string, second: string): string {
  return [
    'export default async function workflow(host) {',
    `  const one = await host.agent({ input: ${JSON.stringify(first)} });`,
    `  if (!one || one.error_kind) throw new Error('The first native child did not complete.');`,
    `  const two = await host.agent({ input: ${JSON.stringify(second)} });`,
    `  if (!two || two.error_kind) throw new Error('The second native child did not complete.');`,
    `  return { words: [one, two] };`,
    '}',
  ].join('\n')
}

museTest('groups the child runs of one native workflow under its heading', async ({ native }) => {
  const { page, modelScript } = native
  const first = modelScript.prompt(wordTask('GROUP_ONE'))
  const second = modelScript.prompt(wordTask('GROUP_TWO'))
  await modelScript.rule(
    {
      name: 'the first workflow child submits its word',
      when: { body: ['Workflow child completion protocol', 'GROUP_ONE'] },
      once: true,
      respond: { toolCalls: [{ id: 'grouping-child-one-submit', name: 'submit_result', namespace: 'muse', arguments: { text: 'GROUP_ONE', notes: null } }] },
    },
    {
      name: 'the second workflow child submits its word',
      when: { body: ['Workflow child completion protocol', 'GROUP_TWO'] },
      once: true,
      respond: { toolCalls: [{ id: 'grouping-child-two-submit', name: 'submit_result', namespace: 'muse', arguments: { text: 'GROUP_TWO', notes: null } }] },
    },
  )
  const start = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-grouping-workflow', workflowScript(first, second))] },
    { text: `The native workflow completed under ${WORKFLOW_NAME}.` },
  )
  await sendMessage(page, modelScript.prompt('Run the native grouping workflow and report its result.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expandBackgroundTasksSection(page)

  const rows = backgroundTaskRows(page)
  const workflow = rows.filter({ hasText: 'Native code' }).first()
  await expect(workflow).toHaveAttribute('data-kind', 'workflow')
  const children = rows.filter({ hasText: /^Child /i })
  await expect(children).toHaveCount(2)
  await expectRowBecomesFinal(page, children.nth(0))
  await expectRowBecomesFinal(page, children.nth(1))
  await expectRowsInWorkflowGroup([children.nth(0), children.nth(1)], /Native code/)
  // A fold child owns no session of its own, so its row opens no child transcript.
  await expect(children.nth(0)).toHaveAttribute('data-child-agent-id', '')
  await expect(children.nth(1)).toHaveAttribute('data-child-agent-id', '')
})

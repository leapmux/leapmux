/**
 * A native Muse workflow appears with its status in the Background tasks sidebar.
 *
 * The native workflow item reports its run and child fold. A separate subagent
 * item supplies the child's actual session, objective and transcript link.
 */
import { expect } from '@playwright/test'
import { codeExecutionToolCall, museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('background-tasks-sidebar: a native workflow shows a live row and its final state', async ({ native }) => {
  const { page, modelScript } = native
  await expectNoRegistryRows(page, native.leapmuxServer)
  const childTask = modelScript.prompt('Reply with the single word SIDEBARWORD.')
  await modelScript.rule({
    name: 'the sidebar child submits its result',
    when: {
      body: ['Workflow child completion protocol'],
    },
    once: true,
    respond: {
      toolCalls: [
        museSubmitResultToolCall('sidebar-child-submit', 'SIDEBARWORD'),
      ],
    },
  })
  const script = [
    'export default async function workflow(host) {',
    `  const one = await host.agent({ input: ${JSON.stringify(childTask)} });`,
    `  if (!one || one.error_kind) throw new Error('The sidebar child did not complete.');`,
    '  return { word: one };',
    '}',
  ].join('\n')
  const start = await modelScript.queue(
    {
      toolCalls: [
        codeExecutionToolCall(native.provider, 'muse-sidebar-workflow', script),
      ],
    },
    { text: 'The native workflow completed.' },
  )
  await sendMessage(page, modelScript.prompt('Run the sidebar workflow and report its word.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  const row = backgroundTaskRows(page).filter({ hasText: 'Native code' }).first()
  await expect(row).toBeVisible()
  await expect(row).toHaveAttribute('data-kind', 'workflow')
  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  const children = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'Reply with the single word SIDEBARWORD.' })
  await expect(children).toHaveCount(1)
  await expect(children).toHaveAttribute('data-child-agent-id', /\S+/)
})

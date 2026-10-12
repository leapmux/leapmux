/**
 * A native Muse workflow appears with its status in the Background tasks sidebar.
 *
 * Muse 1.4.4 runs its background work through the work system: a workflow item
 * carries the run and its children's fold, and a yielded bash command reports
 * `background_running` through its own result with no registry item at all. The
 * workflow row is the provider-reported background process the sidebar draws.
 */
import { expect } from '@playwright/test'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('background-tasks-sidebar: a native workflow shows a live row and its final state', async ({ native }) => {
  const { page, modelScript } = native
  await expectNoRegistryRows(page, native.leapmuxServer)
  const childTask = modelScript.prompt('Reply with the single word SIDEBARWORD.')
  await modelScript.rule({
    name: 'the sidebar child submits its result',
    when: { body: ['Workflow child completion protocol'] },
    once: true,
    respond: { toolCalls: [{ id: 'sidebar-child-submit', name: 'submit_result', namespace: 'muse', arguments: { text: 'SIDEBARWORD', notes: null } }] },
  })
  const script = [
    'export default async function workflow(host) {',
    `  const one = await host.agent({ input: ${JSON.stringify(childTask)} });`,
    `  if (!one || one.error_kind) throw new Error('The sidebar child did not complete.');`,
    '  return { word: one };',
    '}',
  ].join('\n')
  const start = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-sidebar-workflow', script)] },
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
  await expect(backgroundTaskRows(page).filter({ hasText: /^Child /i })).toHaveCount(1)
})

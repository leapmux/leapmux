/**
 * No native route addresses a Muse workflow child: the tool catalog offers no child
 * send tool, and the fold child's row links no child agent, so no composer can reach
 * a child session.
 */
import { expect } from '@playwright/test'
import { museTest } from '../muse-fixtures'
import { expectNoChildAddressingTools, runFoldChildWorkflow } from './subagent-common.muse'

museTest('offers no native route to send input to a workflow child', async ({ native }) => {
  const { catalog, child } = await runFoldChildWorkflow(native, text => native.modelScript.prompt(text))
  expectNoChildAddressingTools(catalog)
  await expect(child).toHaveAttribute('data-child-agent-id', '')
})

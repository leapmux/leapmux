/**
 * Muse 1.4.4 offers no addressable native child session: the model tool catalog
 * holds no spawn tool, and a workflow's children live inside the run's fold with a
 * childId alone, so no child transcript tab can exist.
 */
import { expect } from '@playwright/test'
import { museTest } from '../muse-fixtures'
import { expectNoChildAddressingTools, runFoldChildWorkflow } from './subagent-common.muse'

museTest('a workflow fold child opens no child transcript tab', async ({ native }) => {
  const { catalog, child } = await runFoldChildWorkflow(native, text => native.modelScript.prompt(text))
  expectNoChildAddressingTools(catalog)
  await expect(child).toHaveAttribute('data-kind', 'workflow')
  // The fold child carries no session, so its row links no child agent.
  await expect(child).toHaveAttribute('data-child-agent-id', '')
})

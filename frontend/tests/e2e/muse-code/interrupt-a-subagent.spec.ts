/**
 * No native route cancels one Muse workflow child: the tool catalog offers no child
 * interrupt tool, and the fold child's row links no child agent, so no child tab and
 * no per-child Interrupt control exists.
 */
import { expect } from '@playwright/test'
import { museTest } from '../muse-fixtures'
import { expectNoChildAddressingTools, runFoldChildWorkflow } from './subagent-common.muse'

museTest('offers no native route to interrupt one workflow child', async ({ native }) => {
  const { catalog, child } = await runFoldChildWorkflow(native, text => native.modelScript.prompt(text))
  expectNoChildAddressingTools(catalog)
  await expect(child).toHaveAttribute('data-child-agent-id', '')
})

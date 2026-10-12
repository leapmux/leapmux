/**
 * A running Muse workflow child owns no session, so no separate transcript receives
 * its rows: the fold child's registry row carries the lifecycle alone.
 */
import { expect } from '@playwright/test'
import { museTest } from '../muse-fixtures'
import { expectNoChildAddressingTools, runFoldChildWorkflow } from './subagent-common.muse'

museTest('a running workflow fold child keeps no transcript of its own', async ({ native }) => {
  const { catalog, child, rows } = await runFoldChildWorkflow(native, text => native.modelScript.prompt(text))
  expectNoChildAddressingTools(catalog)
  await expect(child).toHaveAttribute('data-child-agent-id', '')
  // Only the workflow row and its fold child exist; no third row holds a live child.
  await expect(rows.filter({ hasText: /^Child /i })).toHaveCount(1)
})

import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { agentTabs, expectAgentTabCount, tabById } from '../helpers/ui'
import { clickOpaqueAmpTaskRow, exerciseOpaqueAmpTaskLimit } from './opaqueTask'

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for subagent-transcript-tab', async ({ native }) => {
  const { page } = native
  await exerciseOpaqueAmpTaskLimit(native, async (task) => {
    const before = await agentTabs(page).count()
    await expect(task.row).toHaveAttribute('aria-disabled', 'true')
    await clickOpaqueAmpTaskRow(task.row)
    await expectAgentTabCount(page, before)
    await expect(tabById(page, task.parentId)).toHaveAttribute('aria-selected', 'true')
    await expect(task.row).toHaveAttribute('data-status', 'running')
  })
})

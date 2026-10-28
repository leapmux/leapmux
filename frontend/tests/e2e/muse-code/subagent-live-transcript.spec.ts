import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { chatScrollContainer } from '../helpers/ui'
import { museTest } from '../muse-fixtures'
import { expectMuseChildResult, MUSE_CHILD_GATE, MUSE_CHILD_LATER_OUTPUT, MUSE_CHILD_OUTPUT, MUSE_CHILD_PROGRESS, openMuseChild } from './subagent-common.muse'

museTest('streams the running child before completion and receives its later output', async ({ native }) => {
  const { page, modelScript } = native
  await withCleanup(async () => {
    const child = await openMuseChild(native, true)
    await expect(chatScrollContainer(page).getByText(MUSE_CHILD_PROGRESS, { exact: true })).toBeVisible()
    await expect(chatScrollContainer(page).getByText(MUSE_CHILD_OUTPUT, { exact: true })).toBeVisible()
    await expect(chatScrollContainer(page).getByText(MUSE_CHILD_LATER_OUTPUT, { exact: true })).toHaveCount(0)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await child.finish()
    await expect(chatScrollContainer(page).getByText(MUSE_CHILD_LATER_OUTPUT, { exact: true })).toBeVisible()
    await expectMuseChildResult(native)
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
  }, async () => { await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE) })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/516.

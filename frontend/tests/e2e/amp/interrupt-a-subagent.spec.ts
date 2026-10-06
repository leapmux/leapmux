import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { tabById } from '../helpers/ui'
import { clickOpaqueAmpTaskRow, exerciseOpaqueAmpTaskLimit } from './opaqueTask'

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for interrupt-a-subagent', async ({ native }) => {
  const { page } = native
  await exerciseOpaqueAmpTaskLimit(native, async (task) => {
    const parent = await currentNativeAgent(native)
    expect(parent.acceptsInterrupt).toBe(true)
    expect((await readNativeSidebarSnapshot(native, parent.id)).backgroundTasks.every(row => row.childAgentId === '')).toBe(true)
    await expect(task.row).toHaveAttribute('aria-disabled', 'true')
    await clickOpaqueAmpTaskRow(task.row)
    await expect(tabById(page, parent.id)).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator('[data-testid="interrupt-button"]:visible')).toBeVisible()
    await expect(task.row).toHaveAttribute('data-status', 'running')
  })
})

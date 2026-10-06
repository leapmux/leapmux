import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { composerEditor, tabById } from '../helpers/ui'
import { clickOpaqueAmpTaskRow, exerciseOpaqueAmpTaskLimit } from './opaqueTask'

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for send-to-a-subagent', async ({ native }) => {
  const { page } = native
  await exerciseOpaqueAmpTaskLimit(native, async (task) => {
    const parent = await currentNativeAgent(native)
    expect(parent.acceptsMessages).toBe(true)
    expect((await readNativeSidebarSnapshot(native, parent.id)).backgroundTasks.every(row => row.childAgentId === '')).toBe(true)
    await expect(task.row).toHaveAttribute('aria-disabled', 'true')
    await clickOpaqueAmpTaskRow(task.row)
    await expect(tabById(page, parent.id)).toHaveAttribute('aria-selected', 'true')
    await expect(composerEditor(page)).toHaveAttribute('contenteditable', 'true')
    await expect(task.row).toHaveAttribute('data-status', 'running')
  })
})

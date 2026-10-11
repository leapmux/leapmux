import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { messageContents } from '../helpers/ui'
import { exerciseOpaqueAmpTaskLimit } from './opaqueTask'

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for subagent-live-transcript', async ({ native }) => {
  const { page } = native
  await exerciseOpaqueAmpTaskLimit(native, async (task) => {
    expect(JSON.stringify(task.childRequest.body)).toContain('AMPREMOTETASK')
    await expect(messageContents(page).filter({ hasText: task.progress })).toHaveCount(0)
    await expect(task.row).not.toContainText(task.progress)
    const snapshot = await readNativeSidebarSnapshot(native, task.parentId)
    expect(snapshot.backgroundTasks.every(row => row.childAgentId === '')).toBe(true)
    await expect(task.row).toHaveAttribute('data-status', 'running')
  })
})

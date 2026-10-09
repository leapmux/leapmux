import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { finishGeminiChildWithReload, openGeminiRunningChild } from './childScenarios'

geminiTest('stores the actual native child row and its final state after reload', async ({ native }) => {
  const child = await openGeminiRunningChild(native)
  await withCleanup(async () => {
    await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    const running = (await readNativeSidebarSnapshot(native, child.parentId)).backgroundTasks.filter(task => task.childAgentId === child.childId)
    expect(running).toHaveLength(1)
    expect(running[0]).toMatchObject({ id: child.nativeChildId, kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, endedAt: '' })
    await finishGeminiChildWithReload(native, child)
    const completed = (await readNativeSidebarSnapshot(native, child.parentId)).backgroundTasks.filter(task => task.childAgentId === child.childId)
    expect(completed).toHaveLength(1)
    expect(completed[0]).toMatchObject({ id: child.nativeChildId, kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.SUCCEEDED })
    expect(completed[0]?.endedAt).not.toBe('')
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
  }, child.finish)
})

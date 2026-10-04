import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { finishGeminiChildWithReload, openGeminiRunningChild } from './childScenarios'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('stores the actual native child row and its final state after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const child = await openGeminiRunningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    const running = (await readNativeSidebarSnapshot(context, child.parentId)).backgroundTasks.filter(task => task.childAgentId === child.childId)
    expect(running).toHaveLength(1)
    expect(running[0]).toMatchObject({ id: child.nativeChildId, kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, endedAt: '' })
    await finishGeminiChildWithReload(context, child)
    const completed = (await readNativeSidebarSnapshot(context, child.parentId)).backgroundTasks.filter(task => task.childAgentId === child.childId)
    expect(completed).toHaveLength(1)
    expect(completed[0]).toMatchObject({ id: child.nativeChildId, kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.COMPLETED })
    expect(completed[0]?.endedAt).not.toBe('')
    await expect(child.row).toHaveAttribute('data-status', 'completed')
  }, child.finish)
})

import { expect } from '@playwright/test'
import { BackgroundTaskRole, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest('refuses child interruption while its native turn and coordinator remain independent', async ({ native }) => {
  await prepareKimiChildRun(native.page)
  let childId = ''
  let parentId = ''
  await expectUnsupportedSubagent(native, {
    operation: 'interrupt',
    openChild: async () => {
      const context = nativeChildScriptContext(native)
      const identity = heldChildIdentity(context)
      const child = await openRunningNativeChild(native, heldChildOptions(context, identity, {
        child: {
          matcher: kimiChildTurn(identity.task),
        },
      }))
      childId = child.childId
      parentId = child.parentId
      const linked = (await readNativeSidebarSnapshot(native, parentId)).backgroundTasks.filter(row => row.childAgentId === childId)
      const activity = linked.filter(row => row.role === BackgroundTaskRole.CHILD_ACTIVITY)
      expect(activity).toHaveLength(1)
      expect(activity[0]?.status).toBe(BackgroundTaskStatus.RUNNING)
      expect(linked.filter(row => row.role === BackgroundTaskRole.TASK_OUTCOME).length).toBeGreaterThan(0)
      return child
    },
  })
  await retryUntilPass(async () => {
    const linked = (await readNativeSidebarSnapshot(native, parentId)).backgroundTasks.filter(row => row.childAgentId === childId)
    expect({
      activityRuns: linked.some(row => row.role === BackgroundTaskRole.CHILD_ACTIVITY
        && row.status === BackgroundTaskStatus.RUNNING),
      succeededOutcome: linked.some(row => row.role === BackgroundTaskRole.TASK_OUTCOME
        && row.status === BackgroundTaskStatus.SUCCEEDED),
    }).toEqual({ activityRuns: false, succeededOutcome: true })
  })
})

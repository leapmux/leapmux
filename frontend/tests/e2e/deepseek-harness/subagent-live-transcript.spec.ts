import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { selectRunningChildTask } from '../helpers/runningChildProof'
import { waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { registerChildReports } from './scenarios'

deepseekHarnessTest('shows a real child file result while its native model reply remains open', async ({ native, authenticatedDeepseekHarnessWorkspace }) => {
  const childTask = 'DEEPSEEKLIVECHILD read the supplied live file.'
  const parent = await currentNativeAgent(native)
  let childId = ''
  await exerciseLiveChildTranscript(native, {
    childWhen: { user: childTask },
    childTask,
    parentTask: 'Create the scripted live native child.',
    background: true,
    // The native Read result starts with three header rows: `<path>`, `<type>`, and `<content>`. A result view shows only its first three rows until the reader expands it.
    toolProof: { read: { workingDir: authenticatedDeepseekHarnessWorkspace.workingDir, expandResult: true } },
    beforeRelease: async () => {
      await registerChildReports(native)
      const snapshot = await readNativeSidebarSnapshot(native, parent.id)
      const child = selectRunningChildTask(snapshot.backgroundTasks, { parentId: parent.id, rootAgentId: parent.rootAgentId, previousChildIds: new Set() })
      if (!child)
        throw new Error('The live native transcript requires one exact child of its stored parent.')
      childId = child.childAgentId
    },
    afterComplete: () => waitForDeepseekHarnessChildReport(native, childId, parent.id),
  })
})

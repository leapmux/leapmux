import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { selectRunningChildTask } from '../helpers/runningChildProof'
import { waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { nativeContext, registerChildReports } from './scenarios'

deepseekHarnessTest('shows a real child file result while its native model reply remains open', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  const childTask = 'DEEPSEEKLIVECHILD read the supplied live file.'
  const parent = await currentNativeAgent(context)
  let childId = ''
  await exerciseLiveChildTranscript(page, modelScript, {
    provider: context.provider,
    childWhen: { user: childTask },
    childTask,
    parentTask: 'Create the scripted live native child.',
    background: true,
    // The native Read result starts with three header rows: `<path>`, `<type>`, and `<content>`. A result view shows only its first three rows until the reader expands it.
    toolProof: { workingDir: deepseekHarnessWorkspace.workingDir, expandResult: true },
    beforeRelease: async () => {
      await registerChildReports(context)
      const snapshot = await readNativeSidebarSnapshot(context, parent.id)
      const child = selectRunningChildTask(snapshot.backgroundTasks, { parentId: parent.id, rootAgentId: parent.rootAgentId, previousChildIds: new Set() })
      if (!child)
        throw new Error('The live native transcript requires one exact child of its stored parent.')
      childId = child.childAgentId
    },
    afterComplete: () => waitForDeepseekHarnessChildReport(context, childId, parent.id),
  })
})

import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { exerciseOutputByteProgress, PROGRESS_OUTPUT_CALL_ID } from '../helpers/generationProgress'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { toolCallRow } from '../helpers/ui'
import { droidToolRowId } from './toolIdentity'

droidTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native, {
    absentRowText: ['[Process exited with code', 'Error: Command failed (exit code:'],
  })
})

droidTest('updates one tool card with the live output and stores one completed result', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const resolveCallID = () => droidToolRowId(native, { agentId: agent.id, callId: PROGRESS_OUTPUT_CALL_ID })
  await exerciseOutputByteProgress(native, {
    supported: true,
    afterOutputBoundary: async (boundary) => {
      const callID = await resolveCallID()
      // The pending row below the request draws live output while the call runs.
      const live = toolCallRow(native.page, callID, 'pending')
      await expect(live).toHaveCount(1)
      await expect(live).toContainText(boundary.phase === 'first'
        ? boundary.firstLiveTail
        : boundary.secondLiveTail)
      if (boundary.phase === 'second')
        await expect(live).not.toContainText(boundary.firstLiveTail)
      await expect(toolCallRow(native.page, callID)).toHaveCount(0)
    },
  })
  const result = toolCallRow(native.page, await resolveCallID())
  await expect(result).toHaveCount(1)
  await expect(result).toHaveAttribute('data-tool-status', 'completed')
})

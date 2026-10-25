import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { exerciseOutputByteProgress, PROGRESS_OUTPUT_CALL_ID } from '../helpers/generationProgress'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { toolCallRow } from '../helpers/ui'

droidTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native, { absentRowText: ['[Process exited with code', 'Error: Command failed (exit code:'] })
})

droidTest('updates one tool card with the live output and stores one completed result', async ({ native }) => {
  const callID = bashToolCall(native.provider, PROGRESS_OUTPUT_CALL_ID, 'printf progress').id
  await exerciseOutputByteProgress(native, {
    supported: true,
    afterOutputBoundary: async (boundary) => {
      const live = toolCallRow(native.page, callID, 'request')
      await expect(live).toHaveCount(1)
      await expect(live).toContainText(boundary.phase === 'first' ? boundary.firstLiveTail : boundary.secondLiveTail)
      if (boundary.phase === 'second')
        await expect(live).not.toContainText(boundary.firstLiveTail)
      await expect(toolCallRow(native.page, callID)).toHaveCount(0)
    },
  })
  const result = toolCallRow(native.page, callID)
  await expect(result).toHaveCount(1)
  await expect(result).toHaveAttribute('data-tool-status', 'completed')
})

import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { exerciseHeldChildRow, HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('keeps the actual native background task row through completion and reload', async ({ page, native: context }) => {
  await applyPermissionPreset(page, 'bypass')
  // A task item of omp has no description field. With no generated label, omp's own
  // task view shows the subagent ID and a summary of the assignment. The registry
  // row shows the subagent ID and the first line of the assignment.
  await exerciseHeldChildRow(context, {
    rowTitle: HELD_CHILD_NAME,
    heldAnswer: { toolCalls: [ohMyPiYieldToolCall('held-child-yield', HELD_CHILD_REPORT)] },
    rowTexts: [HELD_CHILD_NAME, HELD_CHILD_TASK],
  })
})

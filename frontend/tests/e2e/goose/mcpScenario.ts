import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { controlActions, waitForControlBanner } from '../helpers/ui'

/**
 * Answer the form of the probe server through the browser, with a reload before the submit.
 * Goose asks before the MCP tool runs, and its banner states the server and the tool as `form probe: ask`.
 */
export async function exerciseGooseMcpForm(context: ManagedNativeScenarioContext): Promise<void> {
  const { page } = context
  await exerciseMcpProbeFormRoundTrip(context, {
    callId: 'goose-form',
    reloadBeforeSubmit: true,
    // The click finds the Allow button by its accessible name, which also proves the label of the control.
    approveTool: async () => {
      const permission = await waitForControlBanner(page)
      await expect(permission).toContainText('form probe: ask')
      await controlActions(page).getByRole('button', { name: 'Allow', exact: true }).click()
    },
  })
}

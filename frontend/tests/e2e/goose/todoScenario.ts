import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { controlActions, waitForControlBanner } from '../helpers/ui'

/**
 * Run the to-do list replacement scenario on Goose. Goose writes its checklist through the `todo` extension tool,
 * and the session asks before each write. So the scenario checks Goose's own permission banner, and clicks Allow in
 * the control actions of each write. The click goes through the accessible name of the button, which proves the label
 * of the control.
 */
export async function exerciseGooseTodoListReplacement(context: NativeScenarioContext): Promise<void> {
  await exerciseTodoListReplacement(context, {
    steps: ['Inspect the repository', 'Report their purpose'],
    approveWrite: async (changedStep) => {
      const banner = await waitForControlBanner(context.page)
      await expect(banner).toContainText('todo: todo write')
      await expect(banner).toContainText(changedStep)
      await controlActions(context.page).getByRole('button', { name: 'Allow', exact: true }).click()
    },
  })
}

import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { closeComposerMenus, openSettingsMenu } from '../helpers/ui'

/** Build the scenario context of Codewhale. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.CODEWHALE }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'codewhale', holdWhen: ['app-server'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}

/**
 * Assert the permission posture the agent reports.
 *
 * The status bar draws one mode chip, and Codewhale's is its agent/plan mode, so
 * the posture is read off the checked radio of its own settings group instead.
 */
export async function expectCodewhalePosture(page: Page, posture: string): Promise<void> {
  const group = await openSettingsMenu(page, 'permissionMode')
  await expect(group.locator(`[data-testid="permissionMode-${posture}"] input[type="radio"]`)).toBeChecked()
  await closeComposerMenus(page)
}

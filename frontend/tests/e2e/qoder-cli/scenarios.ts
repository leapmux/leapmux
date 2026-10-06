import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'

/** Build the scenario context of Qoder CLI. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.QODER }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'qodercli', holdWhen: ['-p'] })
}

/** Open this provider's actual child task and hold its native final answer. Qoder CLI needs only the defaults. */
export async function runningChild(context: ManagedNativeScenarioContext): Promise<HeldNativeChild> {
  const script = nativeChildScriptContext(context)
  return openRunningNativeChild(context, heldChildOptions(script, heldChildIdentity(script)))
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}

/** Assert the Qoder mode chip, without matching the separate effort chip. */
export async function expectQoderModeChip(page: Page, mode: string): Promise<void> {
  await expect(page.locator('[data-testid="composer-mode-trigger"]:visible')).toContainText(mode)
}

import type { AgentOpenOverrides } from '../agentSettings'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderWorkingDir } from './providerWorkingDir'
import type { ProviderAgent } from './workspace'
import { expect } from '@playwright/test'
import { currentNativeAgent, selectedAgentTabId } from './nativeScenario'
import { newProviderWorkingDir } from './providerWorkingDir'
import { agentTabs, expectAgentTabCount, openWorkspace } from './ui'
import { openProviderAgent } from './workspace'

/** How {@link openNativeAgent} opens its agent. */
export interface NativeAgentOpenOptions {
  /** The prefix of the name of the new working directory of the agent. */
  directoryPrefix?: string
  /**
   * Use an existing private working directory instead of a directory prefix.
   * This lets the agent read files that the test already created.
   * The provider rule creates it through {@link newNativeWorkingDir}, or `deliberateWorkingDir` states its layout.
   */
  workingDir?: ProviderWorkingDir
  /** The open settings over the pinned settings of the provider. */
  overrides?: AgentOpenOverrides
}

/**
 * Return the opening rule for `context.provider` before creating a working directory.
 * `managedNativeContext` supplies the matching provider rule.
 * A context built by hand can omit the rule or supply another provider's rule. Reject both cases.
 */
export function requireOwnProviderAgent(context: Pick<ManagedNativeScenarioContext, 'provider' | 'providerAgent'>): ProviderAgent {
  // The type requires the agent. A context that a test builds by hand through a cast can still lack it.
  const providerAgent: ProviderAgent | undefined = context.providerAgent
  if (providerAgent === undefined)
    throw new Error(`A native agent of provider ${context.provider} opens by its own rule, and the scenario context states no rule.`)
  if (providerAgent.provider !== context.provider)
    throw new Error(`A native agent of provider ${context.provider} opens by its own rule, not by the rule of provider ${providerAgent.provider}.`)
  return providerAgent
}

/**
 * Create a working directory through the provider's `newProviderWorkingDir` rule. Its name starts with `prefix`.
 * A provider that reads configuration from its surrounding git repository gets a private repository root.
 * Other providers get a fresh directory within the run.
 * Use this helper instead of `createTestDirectory` for native agent directories.
 */
export function newNativeWorkingDir(context: Pick<ManagedNativeScenarioContext, 'provider' | 'providerAgent'>, prefix: string): ProviderWorkingDir {
  return newProviderWorkingDir(requireOwnProviderAgent(context), prefix)
}

/**
 * Open a new agent of `context.provider` and show its workspace. Require the new agent's tab to be selected.
 * Return the agent ID and its working directory.
 * A new directory follows the provider rule through {@link newNativeWorkingDir}.
 * `openProviderAgent` merges and validates option overrides before creating the directory.
 * The next turn uses the selected tab, so an earlier agent that stays selected fails this check.
 */
export async function openNativeAgent(
  context: ManagedNativeScenarioContext,
  options: NativeAgentOpenOptions = {},
): Promise<{ agentId: string, workingDir: ProviderWorkingDir }> {
  const providerAgent = requireOwnProviderAgent(context)
  if (options.workingDir !== undefined && options.directoryPrefix !== undefined)
    throw new Error('A native agent opens in an existing directory or in a new one, not in both.')
  const opened = await openProviderAgent(context.leapmuxServer, context.workspaceId, providerAgent, {
    ...options.overrides,
    ...(options.workingDir === undefined ? { directoryPrefix: options.directoryPrefix ?? 'native-agent-' } : { workingDir: options.workingDir }),
  })
  await openWorkspace(context.page, context.workspaceId)
  await expect.poll(() => selectedAgentTabId(context.page), { message: 'the new native agent is the selected tab' }).toBe(opened.agentId)
  return opened
}

/** Check both provider creation controls against the successfully started Worker agents. */
export async function exerciseProviderAgentButtons(context: ManagedNativeScenarioContext): Promise<void> {
  requireOwnProviderAgent(context)
  const { page, provider } = context
  const original = await currentNativeAgent(context)
  const tabsBefore = await agentTabs(page).count()
  // A UI open records recent provider use and makes its button appear in the tab bar.
  await page.getByTestId('tab-more-menu').click()
  await page.locator(`menu[popover]:visible [data-testid="menu-new-agent-${provider}"]`).click()
  await expectAgentTabCount(page, tabsBefore + 1)
  const fromMenu = await currentNativeAgent(context)
  expect(fromMenu.agentProvider, 'the selected Worker agent uses the requested provider').toBe(provider)
  expect(fromMenu.id, 'the menu selects a new Worker agent').not.toBe(original.id)
  const button = page.getByTestId(`new-agent-button-${provider}`).filter({ visible: true }).first()
  await expect(button).toBeVisible()
  await button.click()
  await expectAgentTabCount(page, tabsBefore + 2)
  const fromButton = await currentNativeAgent(context)
  expect(fromButton.agentProvider, 'the selected Worker agent uses the requested provider').toBe(provider)
  expect([original.id, fromMenu.id], 'the button selects another new Worker agent').not.toContain(fromButton.id)
}

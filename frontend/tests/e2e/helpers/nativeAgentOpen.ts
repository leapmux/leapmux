import type { AgentOpenOverrides } from '../agentSettings'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { expect } from '@playwright/test'
import { selectedAgentTabId } from './nativeScenario'
import { openWorkspace } from './ui'
import { openProviderAgent } from './workspace'

/** How {@link openNativeAgent} opens its agent. */
export interface NativeAgentOpenOptions {
  /** The prefix of the name of the new working directory of the agent. */
  directoryPrefix?: string
  /** An existing private working directory, for an agent that reads a file of the test there. It replaces the prefix. */
  workingDir?: string
  /** The open settings over the pinned settings of the provider. */
  overrides?: AgentOpenOverrides
}

/**
 * Require that `providerAgent` states how an agent of `context.provider` opens. A helper that creates a working
 * directory for a native agent calls it before it creates the directory.
 */
export function requireOwnProviderAgent(context: Pick<ManagedNativeScenarioContext, 'provider'>, providerAgent: ProviderAgent): void {
  if (providerAgent.provider !== context.provider)
    throw new Error(`A native agent of provider ${context.provider} opens by its own rule, not by the rule of provider ${providerAgent.provider}.`)
}

/**
 * Open a new agent of `context.provider`, show its workspace, and require that its tab is the selected tab. Return the
 * ID of the agent and its working directory.
 *
 * `providerAgent` states how an agent of the provider opens, and its provider must be `context.provider`. A new working
 * directory follows the rule of the provider (`newProviderWorkingDir`), so a provider that reads configuration from
 * the git repository around its directory opens in a repository of its own. The open applies the merge rule of
 * `openProviderAgent`, so an override that the rule refuses creates no directory.
 * The next turn goes to the selected tab, so a workspace whose earlier agent stays selected fails here.
 */
export async function openNativeAgent(
  context: ManagedNativeScenarioContext,
  providerAgent: ProviderAgent,
  options: NativeAgentOpenOptions = {},
): Promise<{ agentId: string, workingDir: string }> {
  requireOwnProviderAgent(context, providerAgent)
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

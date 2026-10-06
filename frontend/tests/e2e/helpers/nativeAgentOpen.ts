import type { AgentOpenOverrides } from '../agentSettings'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { expect } from '@playwright/test'
import { selectedAgentTabId } from './nativeScenario'
import { openWorkspace } from './ui'
import { newProviderWorkingDir, openProviderAgent } from './workspace'

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
 * Return how an agent of `context.provider` opens: the `providerAgent` of the context. A helper that creates a working
 * directory for a native agent reads the rule through this function, before it creates the directory.
 *
 * `managedNativeContext` builds a context whose agent is always of its provider. A context built by hand can hold no
 * agent, or the agent of another provider, so the function refuses both.
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
 * Create a new working directory for an agent of `context.provider`, by the rule of the provider
 * (`newProviderWorkingDir`). The name of the directory starts with `prefix`.
 *
 * A provider that reads configuration from the git repository around its directory gets the root of a repository of
 * its own, and every other provider gets a fresh directory of the run. A helper that opens a native agent in a
 * directory of its own creates the directory here, never with `createTestDirectory`.
 */
export function newNativeWorkingDir(context: Pick<ManagedNativeScenarioContext, 'provider' | 'providerAgent'>, prefix: string): string {
  return newProviderWorkingDir(requireOwnProviderAgent(context), prefix)
}

/**
 * Open a new agent of `context.provider`, show its workspace, and require that its tab is the selected tab. Return the
 * ID of the agent and its working directory.
 *
 * A new working directory follows the rule of the provider of the context ({@link newNativeWorkingDir}). The open
 * applies the merge rule of `openProviderAgent`, so an override that the rule refuses creates no directory.
 * The next turn goes to the selected tab, so a workspace whose earlier agent stays selected fails here.
 */
export async function openNativeAgent(
  context: ManagedNativeScenarioContext,
  options: NativeAgentOpenOptions = {},
): Promise<{ agentId: string, workingDir: string }> {
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

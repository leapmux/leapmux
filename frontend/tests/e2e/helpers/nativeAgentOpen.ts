import type { AgentOpenOverrides } from '../agentSettings'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { selectedAgentTabId } from './nativeScenario'
import { createTestDirectory } from './runDirectory'
import { openWorkspace } from './ui'
import { openProviderAgent } from './workspace'

/** How {@link openNativeAgent} opens its agent. */
export interface NativeAgentOpenOptions {
  /** The prefix of the new private working directory of the agent. */
  directoryPrefix?: string
  /** An existing private working directory, for an agent that reads a file of the test there. It replaces the prefix. */
  workingDir?: string
  /** The open settings over the pinned settings of the provider. */
  overrides?: AgentOpenOverrides
}

/**
 * Open a new agent of `context.provider` in a private directory, show its workspace, and require that its tab is the
 * selected tab. Return the ID of the agent and its working directory.
 *
 * The directory is a plain directory of the run, not the working directory rule of the provider's fixture. The open
 * applies the merge rule of `openProviderAgent`, so an override that the rule refuses creates no directory.
 * The next turn goes to the selected tab, so a workspace whose earlier agent stays selected fails here.
 */
export async function openNativeAgent(context: ManagedNativeScenarioContext, options: NativeAgentOpenOptions = {}): Promise<{ agentId: string, workingDir: string }> {
  if (options.workingDir !== undefined && options.directoryPrefix !== undefined)
    throw new Error('A native agent opens in an existing directory or in a new one, not in both.')
  const prefix = options.directoryPrefix ?? 'native-agent-'
  const opened = await openProviderAgent(context.leapmuxServer, context.workspaceId, {
    provider: context.provider,
    prefix,
    workingDir: () => options.workingDir ?? createTestDirectory(prefix),
  }, options.overrides)
  await openWorkspace(context.page, context.workspaceId)
  await expect.poll(() => selectedAgentTabId(context.page), { message: 'the new native agent is the selected tab' }).toBe(opened.agentId)
  return opened
}

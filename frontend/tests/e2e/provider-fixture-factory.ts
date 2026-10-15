/**
 * Shared construction for provider test fixtures.
 * Each provider module supplies its canonical agent and native context constructor.
 */
import type { TestFixture } from '@playwright/test'
import type { AgentOpenOverrides } from './agentSettings'
import type { MockModelRule } from './helpers/mockModelScript'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from './helpers/nativeScenario'
import type { AgentWorkspace, AuthenticatedAgentWorkspaceOptions, ProviderAgent } from './helpers/workspace'
import { test as base } from './fixtures'
import { deliberateWorkingDir } from './helpers/providerWorkingDir'
import { agentWorkspaceFixture, authenticatedAgentWorkspace, openProviderAgent, withTestWorkspace } from './helpers/workspace'
import { ensureWorkerOnline, processTest } from './process-control-fixtures'

/** The fixture of {@link cliSkipFixture}, for the type parameter of a provider's `extend` call. */
export interface CliSkipFixture {
  cliSkip: void
}

/**
 * The provider's scenario context for its main authenticated workspace.
 * The provider's `nativeContext` constructor supplies every provider-specific field.
 */
export interface NativeFixture {
  native: ManagedNativeScenarioContext
}

interface ProviderWorkspaceFixture {
  /** Override this fixture to change the workspace for both the alias and native context. */
  providerWorkspace: AgentWorkspace
}

interface ProviderTestOptions {
  agent: ProviderAgent
  nativeContext: (fixtures: NativeContextFixtures) => Promise<ManagedNativeScenarioContext>
  skipReason: string | null
  modelRules?: readonly MockModelRule[]
}

/** Build a provider test with one workspace shared by its exported alias and native context. */
export function createProviderTest<K extends `authenticated${string}Workspace`>(options: ProviderTestOptions & {
  workspaceName: K
  workspaceOptions?: Omit<AuthenticatedAgentWorkspaceOptions, keyof ProviderAgent>
}) {
  type ProviderFixtures = CliSkipFixture & NativeFixture & ProviderWorkspaceFixture & Record<K, AgentWorkspace>
  const sharedFixtures = {
    cliSkip: cliSkipFixture(options.skipReason),
    modelScript: providerModelScriptFixture(options.modelRules ?? []),
    providerWorkspace: authenticatedAgentWorkspace({
      ...options.workspaceOptions,
      ...options.agent,
    }),
    native: async ({ page, modelScript, leapmuxServer, providerWorkspace }: Omit<NativeContextFixtures, 'workspaceId'> & ProviderWorkspaceFixture, use: (context: ManagedNativeScenarioContext) => Promise<void>) => {
      const fixtures = { page, modelScript, leapmuxServer, workspaceId: providerWorkspace.workspaceId }
      const context = await options.nativeContext(fixtures)
      if (context.provider !== options.agent.provider || context.providerAgent !== options.agent)
        throw new Error('The native context must use the provider that its workspace opened.')
      if (context.workspaceId !== fixtures.workspaceId || context.page !== page || context.modelScript !== modelScript || context.leapmuxServer !== leapmuxServer)
        throw new Error('The native context must retain its workspace and test fixtures.')
      await use(context)
    },
  } satisfies Parameters<typeof base.extend<CliSkipFixture & NativeFixture & ProviderWorkspaceFixture>>[0]
  const fixtures = {
    ...sharedFixtures,
    // Playwright parses literal dependencies from the callback source. A computed dependency cannot work here.
    [options.workspaceName]: async ({ providerWorkspace }: ProviderWorkspaceFixture, use: (workspace: AgentWorkspace) => Promise<void>) => {
      await use(providerWorkspace)
    },
  }
  // TypeScript widens a computed generic key to string. The key above is exactly the workspace alias K.
  return base.extend<ProviderFixtures>(fixtures as Parameters<typeof base.extend<ProviderFixtures>>[0])
}

/**
 * An automatic fixture that skips each test of one provider when the E2E run cannot start the provider's CLI.
 *
 * A missing CLI is a property of the machine, not of model behavior.
 * Every spec of the provider skips with the same reason.
 * Each provider test object registers this automatic fixture as `cliSkip`.
 * It runs before requested fixtures, so a missing CLI skips the test before a workspace fixture starts an agent.
 */
export function cliSkipFixture(reason: string | null): [TestFixture<void, object>, { auto: true }] {
  if (reason !== null && reason.trim() === '')
    throw new Error('A provider skip needs the reason that its CLI is missing.')
  // Playwright reads the fixture names from the first parameter, which must be an object pattern.
  // eslint-disable-next-line no-empty-pattern
  return [async ({}, use, testInfo) => {
    testInfo.skip(reason !== null, reason ?? '')
    await use()
  }, { auto: true }]
}

/** Register provider housekeeping before a scenario can consume an ordered model answer. */
export function providerModelScriptFixture(rules: readonly MockModelRule[]): TestFixture<ModelScript, { modelScript: ModelScript }> {
  return async ({ modelScript }, use) => {
    if (rules.length > 0)
      await modelScript.rule(...rules)
    await use(modelScript)
  }
}

/** Build a provider fixture on the separate Hub and Worker that restart scenarios own. */
export function createProviderProcessTest(options: ProviderTestOptions & {
  openOptions?: AgentOpenOverrides
}) {
  return processTest.extend<CliSkipFixture & NativeFixture>({
    cliSkip: cliSkipFixture(options.skipReason),
    modelScript: providerModelScriptFixture(options.modelRules ?? []),
    workspace: async ({ separateHubWorker, agentWorkingDir }, use) => {
      await ensureWorkerOnline(separateHubWorker)
      await withTestWorkspace(separateHubWorker, options.agent.prefix, async (workspace) => {
        const opened = await openProviderAgent(separateHubWorker, workspace.workspaceId, options.agent, {
          ...options.openOptions,
          ...(agentWorkingDir === undefined
            ? {}
            : {
                workingDir: deliberateWorkingDir(agentWorkingDir, 'The restart test explicitly supplies the native agent directory.'),
              }),
        })
        await use(agentWorkspaceFixture(workspace, opened.agentId, opened.workingDir))
      })
    },
    native: async ({ page, modelScript, separateHubWorker, authenticatedWorkspace }, use) => {
      const context = await options.nativeContext({ page, modelScript, leapmuxServer: separateHubWorker, workspaceId: authenticatedWorkspace.workspaceId })
      if (context.provider !== options.agent.provider || context.providerAgent !== options.agent)
        throw new Error('The process fixture context must use the provider that its workspace opened.')
      await use(context)
    },
  })
}

/**
 * The parts that every provider test object shares.
 * Each `<provider>-fixtures.ts` builds its test object from these parts and from `authenticatedAgentWorkspace` in
 * `helpers/workspace.ts`, and keeps only the facts of its provider.
 */
import type { TestFixture } from '@playwright/test'
import type { AgentOpenOverrides } from './agentSettings'
import type { MockModelRule } from './helpers/mockModelScript'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from './helpers/nativeScenario'
import type { ProviderAgent } from './helpers/workspace'
import { deliberateWorkingDir } from './helpers/providerWorkingDir'
import { agentWorkspaceFixture, openProviderAgent, withTestWorkspace } from './helpers/workspace'
import { ensureWorkerOnline, processTest } from './process-control-fixtures'

/** The fixture of {@link cliSkipFixture}, for the type parameter of a provider's `extend` call. */
export interface CliSkipFixture {
  cliSkip: void
}

/**
 * The scenario context of the provider, with every field of the provider built in.
 * Each provider test object yields it from the `nativeContext` of its provider directory, for its main
 * authenticated workspace.
 */
export interface NativeFixture {
  native: ManagedNativeScenarioContext
}

/**
 * An automatic fixture that skips each test of one provider when the E2E run cannot start the provider's CLI.
 *
 * A missing CLI is a property of the machine, not of model behavior, so every spec of the provider skips with the
 * same reason. Each provider test object registers it under the name `cliSkip`. An automatic fixture runs before
 * the fixtures that a test asks for, so the test skips before a workspace fixture starts an agent.
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
export function createProviderProcessTest(options: {
  agent: ProviderAgent
  nativeContext: (fixtures: NativeContextFixtures) => Promise<ManagedNativeScenarioContext>
  skipReason: string | null
  openOptions?: AgentOpenOverrides
  modelRules?: readonly MockModelRule[]
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

import type { Page } from '@playwright/test'
import type { CliSkipFixture } from './acp-fixture-factory'
import { DEEPSEEK_HARNESS_MODE, DEEPSEEK_HARNESS_OPTION, DEEPSEEK_HARNESS_PERMISSION_PRESET } from '../../src/generated/contracts/deepseek-harness-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { cliSkipFixture } from './acp-fixture-factory'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

export const DEEPSEEK_HARNESS_E2E_SKIP_REASON: string | null = missingBinaryReason('dsh', 'DeepSeek Harness E2E requires the native dsh executable.')

export interface DeepseekHarnessWorkspaceFixture {
  workspaceId: string
  workingDir: string
}

interface DeepseekHarnessServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

function deepseekHarnessWorkspace(permissions: string) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: DeepseekHarnessServer }, use: (fixture: DeepseekHarnessWorkspaceFixture) => Promise<void>) => {
    const workingDir = createGitRepo(createTestDirectory('deepseek-harness-e2e-wd-'), 'repo')
    await withAgentWorkspace(leapmuxServer, {
      provider: AgentProvider.DEEPSEEK_HARNESS,
      prefix: 'deepseek-harness-e2e',
      workingDir: () => workingDir,
      openOptions: { optionValues: { permissionMode: DEEPSEEK_HARNESS_MODE.Act, [DEEPSEEK_HARNESS_OPTION.Permissions]: permissions } },
    }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const deepseekHarnessTest = base.extend<CliSkipFixture & {
  deepseekHarnessWorkspace: DeepseekHarnessWorkspaceFixture
  defaultDeepseekHarnessWorkspace: DeepseekHarnessWorkspaceFixture
}>({
  cliSkip: cliSkipFixture(DEEPSEEK_HARNESS_E2E_SKIP_REASON),
  deepseekHarnessWorkspace: deepseekHarnessWorkspace(DEEPSEEK_HARNESS_PERMISSION_PRESET.DangerFullAccess),
  defaultDeepseekHarnessWorkspace: deepseekHarnessWorkspace(DEEPSEEK_HARNESS_PERMISSION_PRESET.WorkspaceWrite),
})

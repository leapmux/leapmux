import type { Page } from '@playwright/test'
import type { CliSkipFixture } from './acp-fixture-factory'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { cliSkipFixture } from './acp-fixture-factory'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { test as base, expect } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createTestDirectory } from './helpers/runDirectory'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withAgentWorkspace } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

export const COMMAND_CODE_E2E_SKIP_REASON: string | null = missingBinaryReason('command-code', 'Command Code E2E requires the native command-code executable.')

export function createCommandCodeWorkingDir(): string {
  return createGitRepo(createTestDirectory('command-code-e2e-wd-'), 'repo')
}

export interface CommandCodeWorkspaceFixture {
  workspaceId: string
  workingDir: string
}

interface CommandCodeAgentServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

function commandCodeWorkspace(permissionMode: string) {
  return async ({ page, leapmuxServer }: { page: Page, leapmuxServer: CommandCodeAgentServer }, use: (fixture: CommandCodeWorkspaceFixture) => Promise<void>) => {
    const workingDir = createCommandCodeWorkingDir()
    await withAgentWorkspace(leapmuxServer, { provider: AgentProvider.COMMAND_CODE, prefix: 'command-code-e2e', openOptions: { optionValues: { permissionMode } }, workingDir: () => workingDir }, async (workspace) => {
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      await use({ ...workspace, workingDir })
    })
  }
}

export const commandCodeTest = base.extend<CliSkipFixture & { commandCodeWorkspace: CommandCodeWorkspaceFixture, defaultCommandCodeWorkspace: CommandCodeWorkspaceFixture }>({
  cliSkip: cliSkipFixture(COMMAND_CODE_E2E_SKIP_REASON),
  commandCodeWorkspace: commandCodeWorkspace('bypass'),
  defaultCommandCodeWorkspace: commandCodeWorkspace('default'),
})

export async function openCommandCodeAgent(server: CommandCodeAgentServer, workspaceId: string, optionValues: Record<string, string> = {}, workingDir = createCommandCodeWorkingDir()): Promise<{ agentId: string, workingDir: string }> {
  const settings = agentOpenOptions(agentSettings(AgentProvider.COMMAND_CODE))
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, { agentProvider: AgentProvider.COMMAND_CODE, ...settings, optionValues: { ...settings.optionValues, ...optionValues } })
  return { agentId, workingDir }
}

export { expect }

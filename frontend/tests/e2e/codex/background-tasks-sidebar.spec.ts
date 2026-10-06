import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'

codexTest('shows and restores the actual running child row and its completed state', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
  const child = await openRunningNativeChild(context, {
    gate: 'codex-sidebar-child',
    child: { matcher: { body: ['NEW_TASK', 'codex_sidebar_child'] } },
    spawn: spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-sidebar-child', { description: 'codex sidebar child', prompt: modelScript.prompt('Complete the sidebar child task.') }),
  })
  try {
    await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await page.reload()
    await expandBackgroundTasksSection(page)
    await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
    await expect(child.row).toHaveAttribute('data-status', 'running')
  }
  finally {
    await child.finish()
  }
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { messageContents } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('keeps actual child tool output out of the child tab before native completion', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = authenticatedOpencodeWorkspace.workingDir
  if (!directory)
    throw new Error('The native child transcript proof requires a working directory.')
  const marker = `NATIVE_CHILD_ACTUAL_READ_${crypto.randomUUID()}`
  const path = join(directory, 'native-child-live.txt')
  writeFileSync(path, marker)
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  const gate = `native-child-live-${crypto.randomUUID()}`
  const child = await openRunningNativeChild(context, {
    spawn: spawnSubagentToolCall(AgentProvider.OPENCODE, 'native-live-child', { description: 'Read the native child file', prompt: modelScript.prompt('Read the private native child transcript probe, then report your result.') }),
    child: { matcher: { user: '^Read the private native child transcript probe' }, tool: readToolCall(AgentProvider.OPENCODE, 'native-child-read', path) },
    gate,
  })
  await withCleanup(async () => {
    const request = await child.heldRequest()
    expect(nativeToolResult(request, 'native-child-read')).toContain(marker)
    await openChildTabFromRow(page, child.row)
    await expect(messageContents(page).filter({ hasText: marker })).toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'native-child-live.txt' })).toHaveCount(0)
    await expect(child.row).toHaveAttribute('data-status', 'running')
  }, async () => {
    await child.finish()
  })
})

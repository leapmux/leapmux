import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageBubbles } from '../helpers/ui'
import { runCursorNativeOperations } from './nativeExecutionScenario'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
cursorTest('reads and changes actual native file bytes with spaces and shell metacharacters', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const directory = authenticatedCursorWorkspace.workingDir
  if (!directory)
    throw new Error('The native Cursor file proof requires a working directory.')
  const suffix = crypto.randomUUID().replaceAll('-', '')
  const file = join(directory, 'native $(printf WRONG_PATH) source file.txt')
  const created = join(directory, 'new native file with spaces.txt')
  const before = `CURSORBEFORE${suffix}`
  const after = `CURSORAFTER${suffix}`
  const written = `CURSORCREATED${suffix}\n`
  writeFileSync(file, `${before}\n`)
  expect(existsSync(created)).toBe(false)
  await runCursorNativeOperations(context, [
    readToolCall(AgentProvider.CURSOR, 'native-read-before', file),
    editToolCall(AgentProvider.CURSOR, 'native-edit', { path: file, before, after }),
    readToolCall(AgentProvider.CURSOR, 'native-read-after', file),
    writeToolCall(AgentProvider.CURSOR, 'native-create', { path: created, content: written }),
  ], after)
  expect(readFileSync(file, 'utf8')).toBe(`${after}\n`)
  expect(readFileSync(created, 'utf8')).toBe(written)
  const diff = messageBubbles(page).locator('[data-file-diff]').filter({ hasText: after }).first()
  await expect(diff).toBeVisible()
  await expect(diff).toContainText(before)
  const next = await sendNativeAnswer(context, 'Use the existing actual file context and reply once.', 'The native file context probe completed.')
  expect(nativeModelContextText(next)).toContain(before)
  expect(nativeModelContextText(next)).toContain(after)
  expect(nativeModelContextText(next)).toContain(written.trim())
  await page.reload()
  await expect(diff).toBeVisible()
  await expect(diff).toContainText(before)
})

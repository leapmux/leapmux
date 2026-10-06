import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('write tool creates a file and the chat surfaces the path', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  const workingDir = authenticatedPiWorkspace.workingDir
  if (!workingDir)
    throw new Error('The Pi file proof requires a private working directory.')
  const path = join(workingDir, 'pi-test-file.txt')
  expect(existsSync(path)).toBe(false)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.PI, 'write-call', { path, content: 'pi was here' })] },
    { text: 'The native write completed.' },
  )
  await sendMessage(page, modelScript.prompt('Use the native write tool to create the supplied file.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  expect(readFileSync(path, 'utf8')).toBe('pi was here')
  expect(nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'write-call')).toContain('pi-test-file.txt')
  await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'pi-test-file.txt' }).first()).toBeVisible()
})

piTest('reads and changes actual scratch bytes through native file tools', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseFileToolExecution(context)
})

import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { messageBubbles } from '../helpers/ui'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'

cursorTest('executes actual native shell output and preserves a nonzero exit status', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const suffix = crypto.randomUUID().replaceAll('-', '')
  const success = `CURSORSHELL${suffix}42`
  const failure = `CURSORSTDERR${suffix}77`
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native Cursor shell requires its private working directory.')
  const outputFile = join(createNativeToolDirectory(agent.workingDir), 'native-shell-output.txt')
  const quotedOutput = quotePosixShellArgument(outputFile)
  await runCursorNativeOperations(context, [
    bashToolCall(AgentProvider.CURSOR, 'native-shell-success', `printf 'CURSORSHELL${suffix}%s\\n' "$((40 + 2))" > ${quotedOutput} && cat ${quotedOutput}`),
    bashToolCall(AgentProvider.CURSOR, 'native-shell-failure', `printf 'CURSORSTDERR${suffix}%s\\n' "$((70 + 7))" >&2; exit 7`),
  ], failure)
  expect(await cursorNativeToolOutput(context, 'native-shell-success')).toMatchObject({ exitCode: 0, stdout: `${success}\n`, stderr: '' })
  expect(readFileSync(outputFile, 'utf8')).toBe(`${success}\n`)
  expect(existsSync(join(agent.workingDir, 'command-expanded-marker'))).toBe(false)
  expect(await cursorNativeToolOutput(context, 'native-shell-failure')).toMatchObject({ exitCode: 7, stdout: '', stderr: `${failure}\n` })
  await expect(messageBubbles(page).filter({ hasText: failure }).first()).toContainText(/exit\D*7/i)
  const next = await sendNativeAnswer(context, 'Use the actual native shell context and reply once.', 'The shell context probe completed.')
  expect(nativeModelContextText(next)).toContain(success)
  expect(nativeModelContextText(next)).toContain(failure)
})

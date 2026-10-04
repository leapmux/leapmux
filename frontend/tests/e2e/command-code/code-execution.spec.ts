import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseShellToolExecution, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeLoadToolsToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { commandCodeToolCatalog } from './toolCatalog'

commandCodeTest('checks the complete native catalog and its exact executor lookup before a real shell command', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const request = await sendNativeAnswer(context, 'Return the actual native catalog before the executor lookup.', 'The native catalog completed.')
  const names = commandCodeToolCatalog(request)
  expect(names).toContain('shell_command')
  for (const name of ['REPL', 'codemode', 'run_code', 'execute_code'])
    expect(names).not.toContain(name)
  const start = (await modelScript.status()).stepCount
  await modelScript.queue({ toolCalls: [commandCodeLoadToolsToolCall('find-native-code', 'select:REPL,codemode,run_code,execute_code')] }, { text: 'The native executor lookup completed.' })
  await sendMessage(page, modelScript.prompt('Look up the exact native source executor interfaces.'))
  await waitForNativeToolSteps(context, start + 2)
  expect(nativeToolResult((await modelScript.status()).requests.find(record => record.stepIndex === start + 1), 'find-native-code')).toContain('No deferred tool matched')
  await exerciseShellToolExecution(context, { includeFailure: false })
})

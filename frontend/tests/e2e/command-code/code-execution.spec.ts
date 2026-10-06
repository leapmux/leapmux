import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseShellToolExecution, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeLoadToolsToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { commandCodeLoadedToolNames, commandCodeToolCatalog } from './toolCatalog'

commandCodeTest('checks the complete native catalog and its exact executor lookup before a real shell command', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  const request = await sendNativeAnswer(context, 'Return the actual native catalog before the executor lookup.', 'The native catalog completed.')
  const names = commandCodeToolCatalog(request)
  const executors = ['REPL', 'codemode', 'run_code', 'execute_code']
  expect(names).toContain('shell_command')
  for (const name of executors)
    expect(names).not.toContain(name)
  const start = (await modelScript.status()).stepCount
  await modelScript.queue({ toolCalls: [commandCodeLoadToolsToolCall('find-native-code', `select:${executors.join(',')}`)] }, { text: 'The native executor lookup completed.' })
  await sendMessage(page, modelScript.prompt('Look up the exact native source executor interfaces.'))
  await waitForNativeToolSteps(context, start + 2)
  // The native lookup is a fuzzy search, so it loads the closest tool of the catalog when no tool has that name.
  const lookup = nativeToolResult((await modelScript.status()).requests.find(record => record.stepIndex === start + 1), 'find-native-code')
  expect(lookup).toMatch(/^(?:No deferred tool matched|Loaded \d+ tool schema\(s\))/u)
  const loaded = commandCodeLoadedToolNames(lookup)
  for (const name of executors)
    expect(loaded).not.toContain(name)
  await exerciseShellToolExecution(context, { includeFailure: false })
})

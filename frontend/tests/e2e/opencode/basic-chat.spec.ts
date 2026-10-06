import { exerciseBasicChat } from '../helpers/nativeConversation'
import { expectAssistantAnswer, sendMessage, waitForAgentIdle, waitForWorkspaceReady } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('agent reconnects after page reload', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace // fixture trigger

  // Reload the page, then verify a fresh prompt is processed by the
  // reconnected agent — proves reconnection, not just a re-rendered shell.
  await page.reload()
  await waitForWorkspaceReady(page)

  await modelScript.queue({ text: 'hello' })
  await sendMessage(page, modelScript.prompt('Reply with just the word: hello'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page, { answer: /hello/i })
})

opencodeTest('ends a native turn and keeps its answer after reload', async ({ native }) => {
  await exerciseBasicChat(native)
})

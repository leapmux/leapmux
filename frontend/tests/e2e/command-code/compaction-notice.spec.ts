import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chatScrollContainer } from '../helpers/ui'
import { exerciseCommandCodeCompaction } from './compactionScenarios'

commandCodeTest('runs the actual native summarizer and retains the completed compaction notice', async ({ native }) => {
  await exerciseCommandCodeCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})

commandCodeTest('keeps the answer visible after a large preparation message clears the composer', async ({ native }) => {
  const history = Array.from({ length: 60 }, (_, index) => `Native preparation line ${index}.`).join('\n\n')
  await sendNativeAnswer(native, 'Prepare enough native history to scroll.', history)
  await sendNativeAnswer(native, `Recent native task ${'padding '.repeat(18000)}`, 'The large preparation turn completed.', { entry: 'insert' })
  await expect.poll(() => chatScrollContainer(native.page).evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight))
    .toBeLessThanOrEqual(1)
})

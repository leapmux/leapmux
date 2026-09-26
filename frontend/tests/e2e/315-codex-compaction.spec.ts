import { codexTest } from './codex-fixtures'
import { expectCompactionNotice } from './helpers/compaction'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex compaction notice', () => {
  // `/compact` reaches Codex's SendInput and starts a native compaction in
  // place of the answer (see `codex/compaction_test.go`). The summarizer is a
  // housekeeping turn the mock answers from a rule; the notice comes from the
  // CLI's context-compaction item, not from the scripted reply.
  codexTest('a manual compaction draws the context-compacted notice', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await modelScript.rule({
      name: 'compaction-summarizer',
      when: { user: ['compact', 'summary', 'summarize'] },
      respond: { text: 'Earlier work summarized.' },
    })
    await modelScript.queue({ text: 'Ready to compact.' })
    await sendMessage(page, modelScript.prompt('Reply once, then I will compact.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await sendMessage(page, '/compact')
    await waitForAgentIdle(page)

    await expectCompactionNotice(page)
  })
})

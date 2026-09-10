import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { codexTest, expect } from './codex-fixtures'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

// The component tests cover the indicator's visibility transitions.
// One real turn checks provider delivery and the final browser state.
codexTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedCodexWorkspace, page, leapmuxServer }, testInfo) => {
  void authenticatedCodexWorkspace
  try {
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  }
  catch (error) {
    // Capture queue state before fixture cleanup deletes the workspace.
    try {
      const snapshot = execFileSync('sqlite3', ['-readonly', '-json', join(leapmuxServer.dataDir, 'worker', 'worker.db'), 'SELECT * FROM agent_input_queue_state; SELECT agent_id, id, state, error FROM agent_input_queue_items; SELECT agent_id, COUNT(*) AS message_count, MAX(seq) AS last_seq FROM messages GROUP BY agent_id;'])
      await testInfo.attach('input-queue-state', { body: snapshot, contentType: 'text/plain' })
    }
    catch (diagnosticError) {
      throw new AggregateError([error, diagnosticError], 'The test and queue diagnostics failed')
    }
    throw error
  }
})

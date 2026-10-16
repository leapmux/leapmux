import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { codexTest } from '../codex-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'

codexTest('ends an actual native chat turn and restores its answer after reload', async ({ native, leapmuxServer }, testInfo) => {
  try {
    await exerciseBasicChat(native)
  }
  catch (error) {
    // Capture the input queue state before the fixture cleanup deletes the workspace.
    try {
      const snapshot = execFileSync('sqlite3', ['-readonly', '-json', join(leapmuxServer.dataDir, 'worker', 'worker.db'), 'SELECT * FROM agent_input_queue_state; SELECT agent_id, id, state, error FROM agent_input_queue_items; SELECT agent_id, COUNT(*) AS message_count, MAX(seq) AS last_seq FROM messages GROUP BY agent_id;'])
      await testInfo.attach('input-queue-state', { body: snapshot, contentType: 'text/plain' })
      const agent = await currentNativeAgent(native)
      const messages = await readNativeMessageSnapshot(native, agent.id)
      await testInfo.attach('native-message-snapshot', {
        body: JSON.stringify(messages.messages.map(message => ({
          seq: String(message.seq),
          body: nativeMessageBody(message),
          supplement: nativeMessageSupplement(message),
        }))),
        contentType: 'application/json',
      })
    }
    catch (diagnosticError) {
      throw new AggregateError([error, diagnosticError], 'The test and queue diagnostics failed')
    }
    throw error
  }
})

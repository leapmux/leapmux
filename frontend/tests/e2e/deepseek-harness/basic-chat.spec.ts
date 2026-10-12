import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'

deepseekHarnessTest('ends the native turn and keeps its answer after reload', async ({ native }) => {
  // The native `turn/end` event states no duration. The Worker measures the turn from the times of its native start
  // and end events, and adds the duration to the turn end.
  await exerciseBasicChat(native, { timedDivider: true })
})

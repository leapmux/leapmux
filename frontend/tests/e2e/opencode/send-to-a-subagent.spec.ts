import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD } from './childScenario'
import { exerciseOpenCodeFamilyChildSend } from './childSend'

// The Worker loads the child session into OpenCode's ACP adapter and steers it with a second prompt on that session.
// OpenCode adds the message to the running turn, so the child's next request holds it.
opencodeTest('sends a queued message into a running subagent', async ({ native }) => {
  await exerciseOpenCodeFamilyChildSend(native, { profile: OPENCODE_CHILD, readsAfterTurn: false })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.

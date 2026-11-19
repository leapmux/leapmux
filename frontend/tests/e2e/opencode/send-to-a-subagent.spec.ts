import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD, openCodeChildReportRule } from './childScenario'
import { exerciseOpenCodeFamilyChildSend } from './childSend'

// The Worker steers the native child session with another prompt on that session.
opencodeTest('sends a queued message into a running subagent', async ({ native }) => {
  await native.modelScript.rule(openCodeChildReportRule('Answer the message from the child tab'))
  await exerciseOpenCodeFamilyChildSend(native, { profile: OPENCODE_CHILD, readsAfterTurn: false })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.

import { kiloTest } from '../kilo-fixtures'
import { exerciseOpenCodeFamilyChildSend } from '../opencode/childSend'
import { KILO_CHILD } from './childScenario'

// The Worker loads the child session into Kilo's ACP adapter and prompts that session. Kilo holds a prompt that arrives
// during a turn until that turn ends, and the parent's task waits until the child read it.
kiloTest('sends a queued message into a running subagent', async ({ native }) => {
  await exerciseOpenCodeFamilyChildSend(native, { profile: KILO_CHILD, readsAfterTurn: true })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.

import { readFileSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { zcodeTest } from '../zcode-fixtures'

// Run before the workspace fixture starts the agent. Restore this private file
// even when startup or the conversation fails, because the shard shares its HOME.
const personalProviderTest = zcodeTest.extend<{ personalOnlyConfiguration: void }>({
  personalOnlyConfiguration: [
    async ({ leapmuxServer }, use) => {
      const path = join(leapmuxServer.agentEnv.HOME!, '.zcode', 'v2', 'config.json')
      const legacy = readFileSync(path)
      unlinkSync(path)
      try {
        await use()
      }
      finally {
        writeFileSync(path, legacy, { mode: 0o600 })
      }
    },
    { auto: true },
  ],
})

personalProviderTest('starts and completes a native turn with only personal provider configuration', async ({ native }) => {
  await exerciseBasicChat(native, { timedDivider: true })
})

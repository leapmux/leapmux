import { gooseTest } from '../goose-fixtures'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { exerciseGooseDelegateTranscript, GOOSE_CHILD } from './childScenario'

// The delegate transcript proves the read-only composer of a completed child, which states why it accepts no message.
gooseTest('send-to-a-subagent: delegate spawn creates a clickable row with a tool-request transcript', async ({ native }) => {
  await exerciseGooseDelegateTranscript(native)
})

gooseTest('refuses native child send while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openProfiledNativeChild(native, GOOSE_CHILD) })
})

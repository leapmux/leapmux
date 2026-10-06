import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectChildToolOutputDeferred, writeChildMarkerFile } from '../helpers/liveChildTranscript'
import { readToolCall } from '../helpers/providerToolCalls'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { kiloTest } from '../kilo-fixtures'
import { KILO_CHILD } from './childScenario'

/** The call ID of the native Read of the child. */
const READ_CALL_ID = 'native-child-read'

// The provider delivers only the prompt and the report of a child, so the Read result shows in no live row.
kiloTest('keeps actual child tool output out of the child tab before native completion', async ({ native, authenticatedKiloWorkspace }) => {
  const file = writeChildMarkerFile(authenticatedKiloWorkspace.workingDir, 'native-child-live.txt')
  const child = await openProfiledNativeChild(native, KILO_CHILD, { childTool: readToolCall(AgentProvider.KILO, READ_CALL_ID, file.path) })
  await expectChildToolOutputDeferred(native, child, { marker: file.marker, fileName: 'native-child-live.txt', readCallId: READ_CALL_ID, restoredAfterCompletion: false })
})

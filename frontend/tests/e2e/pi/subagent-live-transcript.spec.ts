import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectChildToolOutputDeferred, writeChildMarkerFile } from '../helpers/liveChildTranscript'
import { readToolCall } from '../helpers/providerToolCalls'
import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { piTest } from '../pi-fixtures'
import { PI_CHILD } from './childScenario'

/** The call ID of the native Read of the child. */
const READ_CALL_ID = 'native-child-read'

// The provider delivers only the prompt and the report of a child, so the Read result shows in no live row.
piTest('keeps actual child tool output out of the child tab before native completion', async ({ native, authenticatedPiWorkspace }) => {
  const file = writeChildMarkerFile(authenticatedPiWorkspace.workingDir, 'native-child-live.txt')
  const child = await openProfiledNativeChild(native, PI_CHILD, { childTool: readToolCall(AgentProvider.PI, READ_CALL_ID, file.path) })
  await expectChildToolOutputDeferred(native, child, { marker: file.marker, fileName: 'native-child-live.txt', readCallId: READ_CALL_ID, restoredAfterCompletion: false })
})

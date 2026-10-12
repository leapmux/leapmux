import { diracTest } from '../dirac-fixtures'
import { expectChildToolOutputDeferred, writeChildMarkerFile } from '../helpers/liveChildTranscript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readToolCall } from '../helpers/providerToolCalls'
import { runningChild } from './scenarios'

/** The call ID of the native Read of the child. */
const READ_CALL_ID = 'native-live-read'

// Dirac restores the transcript of a child when the child completes, so the Read result shows after the final report.
diracTest('keeps an actual child read out of live rows and restores it after the final native report', async ({ native }) => {
  const parent = await currentNativeAgent(native)
  const file = writeChildMarkerFile(parent.workingDir, 'native-child-read.txt')
  const child = await runningChild(native, { childTool: readToolCall(native.provider, READ_CALL_ID, file.path) })
  await expectChildToolOutputDeferred(native, child, { marker: file.marker, fileName: 'native-child-read.txt', readCallId: READ_CALL_ID, restoredAfterCompletion: true })
})

import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, READER_SESSION_ID, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readQoderNativeOutput } from './outputFilePaths'

const path = `/native/tool-outputs/session-${READER_SESSION_ID}/native-output.output`
const frame = {
  type: 'user',
  session_id: READER_SESSION_ID,
  message: {
    content: [
      {
        type: 'tool_result',
        tool_use_id: READER_CALL_ID,
        content: READER_PREVIEW,
        is_error: false,
      },
    ],
  },
  tool_use_result: {
    persistedOutput: {
      path,
    },
    stdout: READER_PREVIEW,
    stderr: '',
    exitCode: 0,
  },
}

describe('readQoderNativeOutput', () => {
  runNativeOutputReaderCases({ read: readQoderNativeOutput, frame, path, pointerError: 'The native Qoder pointer belongs to another native result or session.' })
})

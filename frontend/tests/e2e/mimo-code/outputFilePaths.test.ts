import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, READER_SESSION_ID, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readMiMoNativeOutput } from './outputFilePaths'

const path = '/native/tool-output/tool_abc123'
const frame = {
  type: 'message.part.updated',
  properties: {
    part: {
      id: 'part-current',
      sessionID: READER_SESSION_ID,
      callID: READER_CALL_ID,
      type: 'tool',
      tool: 'bash',
      state: {
        status: 'completed',
        output: READER_PREVIEW,
        metadata: {
          truncated: true,
          outputPath: path,
        },
      },
    },
  },
}

describe('readMiMoNativeOutput', () => {
  runNativeOutputReaderCases({ read: readMiMoNativeOutput, frame, path, spanId: 'part-current', pointerError: 'The native MiMo result has no filesystem output pointer.' })
})

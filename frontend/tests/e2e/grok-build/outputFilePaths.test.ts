import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, READER_SESSION_ID, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readGrokNativeOutput } from './outputFilePaths'

const path = `/native/sessions/project/${READER_SESSION_ID}/terminal/${READER_CALL_ID}.log`
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: READER_CALL_ID,
  status: 'completed',
  rawOutput: {
    type: 'Bash',
    output_file: path,
    // Grok sends the preview as an array of UTF-8 bytes.
    output: [...new TextEncoder().encode(READER_PREVIEW)],
  },
}

describe('readGrokNativeOutput', () => {
  runNativeOutputReaderCases({ read: readGrokNativeOutput, frame, path, pointerError: 'The native Grok result has no exact filesystem output pointer.' })
})

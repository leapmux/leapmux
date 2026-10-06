import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readDroidNativeOutput } from './outputFilePaths'

const path = '/native/droid-terminal-current/12345678-1234-1234-1234-123456789abc.log'
const frame = {
  type: 'tool_result',
  toolUseId: READER_CALL_ID,
  messageId: 'message-current',
  content: `${READER_PREVIEW}\nFull command output saved to: ${path} (64 KB)`,
  isError: false,
}

describe('readDroidNativeOutput', () => {
  // Droid stores a tool result in a span of its own, whose ID holds the call ID.
  runNativeOutputReaderCases({ read: readDroidNativeOutput, frame, path, spanId: `droid-tool-${READER_CALL_ID}`, pointerError: 'The native Droid result has no unique terminal output path.' })
})

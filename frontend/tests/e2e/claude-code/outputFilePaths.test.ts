import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, READER_SESSION_ID, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readClaudeNativeOutput } from './outputFilePaths'

const path = `/native/${READER_SESSION_ID}/tool-results/${READER_CALL_ID}.txt`
const frame = {
  type: 'user',
  session_id: READER_SESSION_ID,
  message: {
    content: [
      {
        type: 'tool_result',
        tool_use_id: READER_CALL_ID,
        content: `<persisted-output>\nOutput too large (64 KB). Full output saved to: ${path}\n\nPreview\n${READER_PREVIEW}\n</persisted-output>`,
      },
    ],
  },
}

describe('readClaudeNativeOutput', () => {
  runNativeOutputReaderCases({ read: readClaudeNativeOutput, frame, path, pointerError: 'The native Claude result has no filesystem output pointer.' })
})

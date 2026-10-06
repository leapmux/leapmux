import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readCommandCodeNativeOutput } from './outputFilePaths'

const path = '/native/command/output.log'
const frame = {
  type: 'event',
  event: {
    type: 'tool_completed',
    toolCallId: READER_CALL_ID,
    toolName: 'shell_command',
    result: [
      {
        type: 'text',
        text: `${READER_PREVIEW}\n[full output saved to: ${path} — read it with read_file (offset/limit) or grep]`,
      },
    ],
  },
}

describe('readCommandCodeNativeOutput', () => {
  runNativeOutputReaderCases({ read: readCommandCodeNativeOutput, frame, path, pointerError: 'The native Command Code result has no unique filesystem pointer.' })
})

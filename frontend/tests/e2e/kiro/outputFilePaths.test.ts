import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, READER_SESSION_ID, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readKiroNativeOutput } from './outputFilePaths'

const path = `/native/${READER_SESSION_ID}/tool-outputs/execute_bash-abcd1234.txt`
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: READER_CALL_ID,
  status: 'completed',
  rawOutput: {
    output: READER_PREVIEW,
    message: `Output:\n${READER_PREVIEW}\n\nExit Code: 0`,
    exitCode: 0,
  },
  _meta: {
    kiro: {
      outputTransformation: {
        kind: 'offloaded',
        absFilePath: path,
        totalChars: 100,
      },
    },
  },
}

describe('readKiroNativeOutput', () => {
  runNativeOutputReaderCases({ read: readKiroNativeOutput, frame, path, pointerError: 'The native Kiro result has no exact filesystem output pointer.' })
})

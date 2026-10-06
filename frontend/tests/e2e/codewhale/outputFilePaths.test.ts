import { describe } from 'vitest'
import { READER_CALL_ID, READER_PREVIEW, READER_SESSION_ID, runNativeOutputReaderCases } from '../helpers/nativeOutputReaderCases'
import { readCodewhaleNativeOutput } from './outputFilePaths'

const path = '/native/artifact-session/artifacts/art_current.txt'
const frame = {
  event: 'item.completed',
  thread_id: READER_SESSION_ID,
  payload: {
    item: {
      detail: READER_PREVIEW,
      metadata: {
        tool_use_id: READER_CALL_ID,
        tool_name: 'mcp__probe__inspect',
        artifact_id: 'art_current',
        artifact_session_id: 'artifact-session',
        artifact_relative_path: 'artifacts/art_current.txt',
        spillover_path: path,
      },
    },
  },
}

describe('readCodewhaleNativeOutput', () => {
  runNativeOutputReaderCases({ read: readCodewhaleNativeOutput, frame, path, pointerError: 'The native Codewhale result has no filesystem pointer.' })
})

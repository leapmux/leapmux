import type { FileChangeRequest, FileChangeResult } from './fileChange'

/** One move carries one entry: `{ filePath: destination, previousPath: source, operation: 'move' }`. */
export type MoveRequest = FileChangeRequest
export type MoveResult = FileChangeResult

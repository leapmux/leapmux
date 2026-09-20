import type { FileChangeRequest, FileChangeResult } from './fileChange'

/** One write carries one entry, with `operation: 'add'`. */
export type WriteRequest = FileChangeRequest
export type WriteResult = FileChangeResult

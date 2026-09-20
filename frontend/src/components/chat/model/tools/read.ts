import type { ReadFileResult } from '../readFileResult'

export interface ReadRequest { path: string, offset?: number, limit?: number }
export type ReadResult = ReadFileResult

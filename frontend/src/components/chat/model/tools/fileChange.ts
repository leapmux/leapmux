import type { FileEditDiff } from '../fileEditDiff'

/** The changes a call ASKED for. They establish nothing about the file. */
export interface FileChangeRequest { changes: FileEditDiff[], replaceAll?: boolean }
/** The changes that LANDED, one per file operation. */
export interface FileChangeResult { changes: FileEditDiff[] }

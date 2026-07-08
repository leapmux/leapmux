import { Code, ConnectError } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { isWorkerUnreachable } from '~/api/workerErrors'

// isWorkerUnreachable backs the tab-close fallback for orphaned
// workers (useTabOperations.handleTabClose). The contract MUST stay
// in lockstep with the CLI's `isWorkerUnreachable` in
// `backend/internal/cli/remote/cmd/preflight.go` — drift between
// the two means one transport closes orphan tabs and the other
// doesn't.

describe('isworkerunreachable', () => {
  it('matches the four existence/auth codes', () => {
    const codes: Code[] = [Code.NotFound, Code.PermissionDenied, Code.Unauthenticated, Code.Unavailable]
    for (const code of codes) {
      const err = new ConnectError('worker gone', code)
      expect(isWorkerUnreachable(err), `code=${code}`).toBe(true)
    }
  })

  it('does not match transient/internal codes', () => {
    const codes: Code[] = [Code.Internal, Code.DeadlineExceeded, Code.Unknown, Code.ResourceExhausted, Code.Aborted]
    for (const code of codes) {
      const err = new ConnectError('boom', code)
      expect(isWorkerUnreachable(err), `code=${code}`).toBe(false)
    }
  })

  it('returns false for non-connect errors', () => {
    expect(isWorkerUnreachable(new Error('bare error'))).toBe(false)
    expect(isWorkerUnreachable('string')).toBe(false)
    expect(isWorkerUnreachable(null)).toBe(false)
    expect(isWorkerUnreachable(undefined)).toBe(false)
    expect(isWorkerUnreachable({ code: Code.NotFound })).toBe(false)
  })
})

import type { JsonValue } from '@bufbuild/protobuf'
import { fromJson } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AdminUserService } from '../../../src/generated/proto/leapmux/v1/admin_pb'
import { AuthService } from '../../../src/generated/proto/leapmux/v1/auth_pb'
import { UserService } from '../../../src/generated/proto/leapmux/v1/user_pb'
import { procedureOf } from '../../../src/lib/rpcProcedure'
import { TEST_ADMIN_PASSWORD } from './api'
import { mintCLITokenForAdmin } from './cli'

const hubUrl = 'http://cli-hub.test'

/** Answer each hub request by its procedure, and record each request with its body. */
function hub(answer: (procedure: string) => Response) {
  const requests: Array<{ procedure: string, cookie: string | undefined, body: JsonValue }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const procedure = new URL(String(input)).pathname
    requests.push({
      procedure,
      cookie: (init?.headers as Record<string, string> | undefined)?.Cookie,
      body: JSON.parse(String(init?.body)) as JsonValue,
    })
    return answer(procedure)
  }))
  return requests
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('mintCLITokenForAdmin', () => {
  it('issues the token for the session user, which it selects by ID', async () => {
    const currentUser = {
      sessionReport: {
        user: { id: 'admin-id', username: 'admin' },
      },
    }
    fromJson(AuthService.method.getCurrentUser.output, currentUser)
    // The reply carries no token, so the helper stops before it writes a credential file.
    const requests = hub(procedure => procedure === procedureOf(AuthService.method.getCurrentUser)
      ? Response.json(currentUser)
      : Response.json({}))
    await expect(mintCLITokenForAdmin({ hubUrl, adminToken: 'leapmux-session=admin' }))
      .rejects
      .toThrow('mintCLITokenForAdmin: no accessToken in IssueAPIToken response')
    expect(requests.map(request => request.procedure)).toEqual([
      procedureOf(AuthService.method.getCurrentUser),
      procedureOf(UserService.method.elevateSession),
      procedureOf(AdminUserService.method.issueAPIToken),
    ])
    expect(requests[1]?.body).toEqual({ currentPassword: TEST_ADMIN_PASSWORD })
    const issue = requests[2]
    expect(issue?.cookie).toBe('leapmux-session=admin')
    // The decode refuses an unknown field, so the old flat `userId` fails here.
    expect(() => fromJson(AdminUserService.method.issueAPIToken.input, issue?.body ?? null)).not.toThrow()
    expect(issue?.body).toMatchObject({
      user: { id: 'admin-id' },
      ttlSeconds: '3600',
    })
    expect(issue?.body).not.toHaveProperty('userId')
  })
})

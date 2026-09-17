import { describe, expect, it } from 'vitest'
import { codexExtractControl } from './extractControl'

function control(payload: Record<string, unknown>) {
  return codexExtractControl({ payload } as never)
}

describe('codexExtractControl', () => {
  it.each([
    ['item/commandExecution/requestApproval', 'Command Execution'],
    ['item/fileChange/requestApproval', 'File Change'],
  ])('titles a %s card', (method, title) => {
    const request = control({ method, params: { reason: 'needs approval', command: 'ls', cwd: '/repo' } })
    expect(request).toMatchObject({ kind: 'permission', permission: { title, reason: 'needs approval', command: 'ls', workingDirectory: '/repo' } })
  })

  // The method comes straight off the wire, and the title table is a plain object. A
  // method that identifies an `Object.prototype` member answered with a FUNCTION, which
  // the card would then draw as its own title.
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty'])('states no title for a method called %s', (method) => {
    const request = control({ method, params: {} })
    expect(request?.kind).toBe('permission')
    expect(request?.kind === 'permission' && request.permission.title).toBeUndefined()
  })

  // This one asks for a SET of permissions rather than one operation, and the set is
  // the content the card draws.
  it('states no title for a permissions approval, and draws the set it asks for', () => {
    const request = control({ method: 'item/permissions/requestApproval', params: { permissions: { network: { outbound: true } } } })
    expect(request?.kind === 'permission' && request.permission.title).toBeUndefined()
    expect(request?.kind === 'permission' && request.permission.input).toEqual({ network: { outbound: true } })
  })

  it('reads the plan-mode prompt as a plan rather than a permission', () => {
    expect(control({ request: { tool_name: 'CodexPlanModePrompt' } })).toEqual({ kind: 'plan' })
  })
})

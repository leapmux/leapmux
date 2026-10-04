import { describe, expect, it } from 'vitest'
import { codexExtractControl } from './extractControl'

function control(payload: Record<string, unknown>) {
  return codexExtractControl({ payload })
}

describe('codexExtractControl', () => {
  it.each([
    ['item/commandExecution/requestApproval', 'Command Execution'],
    ['item/fileChange/requestApproval', 'File Change'],
  ])('titles a %s card', (method, title) => {
    const request = control({ method, params: { reason: 'needs approval', command: 'ls', cwd: '/repo' } })
    expect(request).toMatchObject({ kind: 'permission', permission: { title, reason: 'needs approval', command: 'ls', workingDirectory: '/repo' } })
  })

  // The native method can match an Object.prototype key.
  // Require no inherited title so the control card cannot display a prototype function.
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty'])('states no title for a method called %s', (method) => {
    const request = control({ method, params: {} })
    expect(request?.kind).toBe('permission')
    expect(request?.kind === 'permission' && request.permission.title).toBeUndefined()
  })

  // A permissions approval displays the requested permissions without an operation title.
  it('states no title for a permissions approval, and draws the set it asks for', () => {
    const request = control({ method: 'item/permissions/requestApproval', params: { permissions: { network: { outbound: true } } } })
    expect(request?.kind === 'permission' && request.permission.title).toBeUndefined()
    expect(request?.kind === 'permission' && request.permission.input).toEqual({ network: { outbound: true } })
  })

  it('reads the plan-mode prompt as a plan rather than a permission', () => {
    expect(control({ request: { tool_name: 'CodexPlanModePrompt' } })).toEqual({ kind: 'plan' })
  })

  it('keeps the native file-system permissions and drops unrelated grant fields', () => {
    const fileSystem = { read: ['/native path/read.txt'], write: [] }
    const permissions = { fileSystem, unrelated: { approve: true } }
    const request = control({ method: 'item/permissions/requestApproval', params: { permissions } })
    expect(request?.kind === 'permission' && request.permission.input).toEqual({ fileSystem })
    expect(permissions).toEqual({ fileSystem, unrelated: { approve: true } })
  })

  it.each([null, [], false, 0, 'invalid'])('returns an empty permission input for malformed params: %j', (params) => {
    const request = control({ method: 'item/permissions/requestApproval', params })
    expect(request?.kind === 'permission' && request.permission.input).toEqual({})
  })

  it.each([null, [], false, 0, 'invalid'])('returns an empty permission input for malformed grants: %j', (permissions) => {
    const request = control({ method: 'item/permissions/requestApproval', params: { permissions } })
    expect(request?.kind === 'permission' && request.permission.input).toEqual({})
  })

  it('drops non-object network and file-system grants', () => {
    const request = control({ method: 'item/permissions/requestApproval', params: { permissions: { network: false, fileSystem: [] } } })
    expect(request?.kind === 'permission' && request.permission.input).toEqual({})
  })
})

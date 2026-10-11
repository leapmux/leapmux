import { describe, expect, it } from 'vitest'
import { zcodeMcpConnectFrame } from './mcpScenario'

describe('zcodeMcpConnectFrame', () => {
  it('keeps the native workspace path and request ID without shell interpretation', () => {
    const path = '/private/project $(printf WRONG_PATH) with spaces'
    expect(JSON.parse(zcodeMcpConnectFrame('native-request', path))).toEqual({
      id: 'native-request',
      method: 'mcp/list',
      params: { workspace: { workspacePath: path, workspaceKey: path }, mode: 'connect' },
    })
  })

  it.each([{ id: '', path: '/project' }, { id: 'request', path: '' }])('rejects an absent native identifier in $id / $path', ({ id, path }) => {
    expect(() => zcodeMcpConnectFrame(id, path)).toThrow('requires a request ID and working directory')
  })
})

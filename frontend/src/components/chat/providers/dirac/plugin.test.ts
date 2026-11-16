import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { describeACPCallRows } from '../acp/testUtils'
import { providerFor } from '../registry'
import './plugin'

describe('dirac provider', () => {
  it('maps Bypass to native yolo without changing the model or Plan mode', () => {
    const plugin = providerFor(AgentProvider.DIRAC)
    expect(plugin?.controls?.permissionPresets?.bypass).toEqual({
      sets: { yolo: 'true' },
    })
    expect(plugin?.controls?.permissionPresets?.smart).toBeUndefined()
    expect(plugin?.configuration?.planMode).toMatchObject({ groupKey: 'permissionMode', planValue: 'plan', defaultValue: 'act' })
  })
})

// Dirac's edit_file states its change as `files[].edits[]`, and only the answer states
// the diff that landed. The request row draws the flattened change that the call asked
// for, and the result row carries the landed diff.
describe('dirac call rows', () => {
  describeACPCallRows(AgentProvider.DIRAC, [
    {
      name: 'an edit_file that answers with the landed diff',
      request: {
        sessionUpdate: 'tool_call',
        toolCallId: 'dirac-edit',
        kind: 'edit',
        title: 'Edit note.txt',
        status: 'pending',
        rawInput: {
          tool: 'edit_file',
          files: [
            {
              path: '/w/note.txt',
              edits: [
                { old_string: 'dirac-before', new_string: 'dirac-after' },
              ],
            },
          ],
        },
      },
      result: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'dirac-edit',
        status: 'completed',
        content: [
          {
            type: 'diff',
            path: '/w/note.txt',
            oldText: 'dirac-before\n',
            newText: 'dirac-after\n',
          },
        ],
      },
      requestCall: {
        kind: 'edit',
        status: 'pending',
        request: {
          changes: [
            { filePath: '/w/note.txt', oldStr: 'dirac-before', newStr: 'dirac-after' },
          ],
        },
      },
      resultCall: {
        kind: 'edit',
        status: 'completed',
        request: {
          changes: [
            { filePath: '/w/note.txt', oldStr: 'dirac-before', newStr: 'dirac-after' },
          ],
        },
        result: {
          changes: [
            { filePath: '/w/note.txt', oldStr: 'dirac-before\n', newStr: 'dirac-after\n' },
          ],
        },
      },
    },
  ])
})

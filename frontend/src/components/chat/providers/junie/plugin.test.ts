import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'
import { describeACPCallRows, describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { junieOutputFilePaths } from './extractors/outputFilePaths'
import { juniePermissionPresets } from './permissionPresets'

import './plugin'

describe('junie provider', () => {
  describeACPProviderBasics(AgentProvider.JUNIE, {
    text: true,
    image: true,
    pdf: false,
    audio: false,
    binary: false,
  })

  it('registers its own reader of output file paths', () => {
    expect(providerFor(AgentProvider.JUNIE)?.transcript.outputFilePaths).toBe(junieOutputFilePaths)
  })

  it('carries plan mode on the permission-mode axis', () => {
    const configuration = providerFor(AgentProvider.JUNIE)?.configuration
    expect(configuration?.triggerModeGroupKey).toBe(OPTION_ID_PERMISSION_MODE)
    expect(configuration?.planMode).toBeDefined()
  })

  it('offers the Brave mode permission presets', () => {
    expect(providerFor(AgentProvider.JUNIE)?.controls?.permissionPresets).toBe(juniePermissionPresets)
  })
})

// Junie can state the change of a search_replace as a content diff with no arguments.
// The request row draws the change that its own frame states, and the result row
// carries the diff that the answer confirms.
describe('junie call rows', () => {
  describeACPCallRows(AgentProvider.JUNIE, [
    {
      name: 'a search_replace that the answer confirms with a diff',
      request: {
        sessionUpdate: 'tool_call',
        toolCallId: 'junie-edit',
        kind: 'edit',
        title: 'Edit note.txt',
        status: 'pending',
        rawInput: { file_path: '/w/note.txt', search: 'junie-before', replace: 'junie-after' },
      },
      result: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'junie-edit',
        status: 'completed',
        content: [
          {
            type: 'diff',
            path: '/w/note.txt',
            oldText: 'junie-before\n',
            newText: 'junie-after\n',
          },
        ],
      },
      requestCall: {
        kind: 'edit',
        status: 'pending',
        request: {
          changes: [
            { filePath: '/w/note.txt', oldStr: 'junie-before', newStr: 'junie-after' },
          ],
        },
      },
      resultCall: {
        kind: 'edit',
        status: 'completed',
        result: {
          changes: [
            { filePath: '/w/note.txt', oldStr: 'junie-before\n', newStr: 'junie-after\n' },
          ],
        },
      },
    },
  ])
})

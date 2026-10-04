import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { classSelector } from '~/test-support/composedClass'
import { toolCallRow } from '../../../model/row'
import { SYNTHETIC_TOOL_LIFECYCLE } from '../../../model/toolCallLifecycle'
import { ToolMessage } from '../../../results/ToolMessage'
import { toolUseHeader } from '../../../toolStyles.css'
import { codebuddyToolCall } from './toolCommon'

describe('codebuddyToolCall rendering', () => {
  it('renders the native JavaScript source and a failure header for the exact REPL error', () => {
    const source = 'throw new Error("computed-" + (70 + 7))'
    const output = JSON.stringify({ stdout: '', stderr: '', error: 'Error: computed-77' })
    const call = codebuddyToolCall({ callId: 'render-native-repl', toolName: 'REPL', args: { code: source }, resultText: output, isError: false, lifecycle: { ...SYNTHETIC_TOOL_LIFECYCLE } })
    const { container } = render(() => <ToolMessage row={toolCallRow(call, 'result', { request: false, result: true })} />)
    const headers = [...container.querySelectorAll(classSelector(toolUseHeader))].map(header => header.textContent)
    expect(headers).toContain('Error')
    expect(container.textContent).toContain(source)
    expect(container.textContent).toContain('computed-77')
  })
})

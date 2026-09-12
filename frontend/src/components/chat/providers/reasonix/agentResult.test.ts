import { describe, expect, it } from 'vitest'
import { reasonixAgentResult } from './agentResult'

function taskResult(input: Record<string, unknown>, output: string, status: unknown) {
  return reasonixAgentResult({ toolName: 'task', input, output, status })
}

const header = 'Subagent reference: sa_example\nSubagent outcome: status=completed retryable=false'

describe('reasonix agent result', () => {
  it('lets cancellation override a stored partial outcome', () => {
    const source = taskResult({}, 'Subagent outcome: status=partial retryable=true error_code=max_steps\n\nFinal answer:\nPartial report', 'cancelled')
    expect(source.outcome).toBe('stopped')
    expect(source.status).toBe('stopped')
  })

  it('preserves a native partial outcome when ACP reports tool failure', () => {
    const source = taskResult({}, 'Subagent outcome: status=partial retryable=true error_code=max_steps\n\nFinal answer:\nPartial report', 'failed')
    expect(source.outcome).toBe('unknown')
    expect(source.status).toBe('partial')
    expect(source.body).toBe('Partial report')
  })

  it('does not infer success from an unknown native outcome or background response', () => {
    expect(taskResult({}, 'Subagent reference: sa_example\nSubagent outcome: status=new_state retryable=false', 'completed').outcome).toBe('unknown')
    expect(taskResult({ run_in_background: true }, 'Unrecognized response', 'completed').outcome).toBe('unknown')
  })

  it.each([['completed', 'completed'], ['partial', 'unknown'], ['failed', 'failed'], ['cancelled', 'stopped']])('reads the %s outcome of an ephemeral subagent without a reference', (status, outcome) => {
    const source = taskResult({}, `Subagent outcome: status=${status} retryable=true error_code=native_code\n\nFinal answer:\nReport`, 'completed')
    expect(source.outcome).toBe(outcome)
    expect(source.agentId).toBe('')
    expect(source.body).toBe('Report')
    expect(source.metadata).toContainEqual({ label: 'Retryable', value: 'Yes' })
    expect(source.metadata).toContainEqual({ label: 'Error code', value: 'native_code' })
  })

  it('keeps a background launch running until a final native outcome arrives', () => {
    const output = 'Started background task "job-1" (Inspect sample). It runs across turns; collect its final answer with wait (or wait will return it once done), and you\'ll be notified when it finishes.'
    const source = taskResult({ run_in_background: true }, output, 'completed')
    expect(source.outcome).toBe('running')
    expect(source.body).toBe(output)
    expect(taskResult({ run_in_background: true }, `${header}\n\nFinal answer:\nReport`, 'completed').outcome).toBe('completed')
    expect(taskResult({ run_in_background: true }, 'Launch failed', 'failed').outcome).toBe('failed')
  })

  it('keeps the ancestor reference when it removes native fork guidance', () => {
    const guidance = 'Forked from: sa_parent\nThe requested ref resolves to an ancestor conversation transcript, so the framework continues a copy owned by the current conversation. To continue this copied subagent transcript in a later call, pass sa_example as `continue_from`. Start a fresh subagent when the next task is independent.'
    const source = taskResult({}, `${header}\n\n${guidance}\n\nFinal answer:\nReport`, 'completed')
    expect(source.body).toBe('Report')
    expect(source.metadata).toContainEqual({ label: 'Forked from', value: 'sa_parent' })
  })
  it('preserves unknown header details before the final answer', () => {
    const output = `${header}\n\nNew native detail: retain this\n\nFinal answer:\nReport body`
    const source = taskResult({}, output, 'completed')
    expect(source.body).toContain('New native detail: retain this')
    expect(source.body).toContain('Report body')
  })

  it('preserves unknown output after a recognized header without an answer marker', () => {
    expect(taskResult({}, `${header}\n\nKeep this output`, 'completed').body).toContain('Keep this output')
  })

  it('preserves malformed or unknown headers as ordinary output', () => {
    const output = 'Subagent reference: ../wrong\nSubagent outcome: status=completed retryable=false\n\nFinal answer:\nReport'
    expect(taskResult({}, output, 'completed').body).toBe(output)
  })

  it('preserves an explicit empty final answer', () => {
    expect(taskResult({}, `${header}\n\nFinal answer:\n`, 'completed').body).toBe('')
  })

  it.each([['partial', 'unknown'], ['failed', 'failed'], ['cancelled', 'stopped']])('renders the native %s outcome', (status, outcome) => {
    const output = `Subagent reference: sa_example\nSubagent outcome: status=${status} retryable=true error_code=native_code\n\nFinal answer:\nReport`
    const source = taskResult({}, output, 'completed')
    expect(source.outcome).toBe(outcome)
    expect(source.metadata).toContainEqual({ label: 'Error code', value: 'native_code' })
    expect(source.body).toBe('Report')
  })
})

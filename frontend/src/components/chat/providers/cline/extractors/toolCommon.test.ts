import { describe, expect, it } from 'vitest'
import { clineToolFinishRow, clineToolStartRow } from '../toolResults.fixtures'
import { clineOperations, clineSideCallId, clineToolFinish, clineToolStart, errorText, outputText, parseJSON } from './toolCommon'

describe('clineToolStart', () => {
  it('reads the call a start row states', () => {
    expect(clineToolStart(clineToolStartRow('read_files', { files: [] }, 'c1'))).toEqual({ id: 'c1', name: 'read_files', input: { files: [] } })
  })

  it('reads arguments that are not an object as none', () => {
    const row = clineToolStartRow('read_files', {}, 'c1')
    ;(row.payload as Record<string, unknown>).input = 'x'
    expect(clineToolStart(row)?.input).toEqual({})
  })

  it('reads no call from a row that states no id or no tool, or another row', () => {
    expect(clineToolStart({ event: 'tool.started', payload: { toolName: 'read_files' } })).toBeNull()
    expect(clineToolStart({ event: 'tool.started', payload: { toolCallId: 'c1' } })).toBeNull()
    expect(clineToolStart(clineToolFinishRow('read_files', []))).toBeNull()
    expect(clineToolStart(null)).toBeNull()
  })
})

describe('clineToolFinish', () => {
  it('reads the end a finish row states', () => {
    expect(clineToolFinish(clineToolFinishRow('read_files', ['x'], undefined, 'c1'))).toEqual({ id: 'c1', name: 'read_files', output: ['x'], error: '' })
  })

  it('reads the words of a refusal out of Cline\'s JSON error', () => {
    expect(clineToolFinish(clineToolFinishRow('editor', { error: 'No.' }, '{"error":"No."}'))?.error).toBe('No.')
  })

  it('reads no end from a row with no id', () => {
    expect(clineToolFinish({ event: 'tool.finished', payload: { toolName: 'editor' } })).toBeNull()
  })

  // The editor and the patch tool state a failure inside their one record, and Cline
  // states no error for the call. That record is the whole call.
  it('reads the error of a call whose one record states a failure', () => {
    const record = { query: 'edit:/w/a.ts', result: '', error: 'Editor operation failed: no match', success: false }
    expect(clineToolFinish(clineToolFinishRow('editor', record))?.error).toBe('Editor operation failed: no match')
    expect(clineToolFinish(clineToolFinishRow('editor', JSON.stringify(record)))?.error).toBe('Editor operation failed: no match')
    // A failure with no words still fails the call.
    expect(clineToolFinish(clineToolFinishRow('editor', { query: 'edit:/w/a.ts', result: '', success: false }))?.error).toBe('The operation failed.')
  })

  it('reads the error Cline states for the call before the error of its record', () => {
    const record = { query: 'edit:/w/a.ts', result: '', error: 'inner', success: false }
    expect(clineToolFinish(clineToolFinishRow('editor', record, 'outer'))?.error).toBe('outer')
  })

  // A list states one record for each operation, so one failed operation is not the
  // failure of the call: its error stays in its own record.
  it('reads no error from a list whose operation failed, or from a record that succeeded', () => {
    expect(clineToolFinish(clineToolFinishRow('read_files', [{ query: '/w/a.ts', result: '', error: 'Not found', success: false }]))?.error).toBe('')
    expect(clineToolFinish(clineToolFinishRow('editor', { query: 'edit:/w/a.ts', result: 'Edited', success: true }))?.error).toBe('')
    expect(clineToolFinish(clineToolFinishRow('spawn_agent', { text: 'x', success: false }))?.error).toBe('')
  })
})

describe('errorText', () => {
  it('keeps plain words, and reads JSON that is not a refusal whole', () => {
    expect(errorText('boom')).toBe('boom')
    expect(errorText('{"other":1}')).toBe('{"other":1}')
    expect(errorText('{not json')).toBe('{not json')
    expect(errorText(undefined)).toBe('')
    expect(errorText(3)).toBe('')
  })

  it('reads the words of a refusal that space surrounds', () => {
    expect(errorText('  {"error":"No."}\n')).toBe('No.')
    expect(errorText('  boom  ')).toBe('boom')
  })

  // Only a string holds the words, so an `error` of another type leaves the JSON whole.
  it('reads a refusal whose error is not text whole', () => {
    expect(errorText('{"error":3}')).toBe('{"error":3}')
    expect(errorText('{"error":{"message":"No."}}')).toBe('{"error":{"message":"No."}}')
  })
})

describe('clineSideCallId', () => {
  const side = (parentObject: Record<string, unknown>) => ({ rawText: '', topLevel: parentObject, parentObject, wrapper: null })

  it('reads the id of either side', () => {
    expect(clineSideCallId(side(clineToolStartRow('x', {}, 'c1')))).toBe('c1')
    expect(clineSideCallId(side(clineToolFinishRow('x', [], undefined, 'c2')))).toBe('c2')
    expect(clineSideCallId(side({ event: 'assistant.finished', payload: {} }))).toBe('')
    expect(clineSideCallId(undefined)).toBe('')
  })
})

describe('clineOperations', () => {
  it('reads a list, one record, and the JSON text of either', () => {
    const record = { query: 'q', result: 'r', success: true }
    expect(clineOperations([record])).toEqual([{ query: 'q', result: 'r', error: '', success: true }])
    expect(clineOperations(record)).toEqual([{ query: 'q', result: 'r', error: '', success: true }])
    expect(clineOperations(JSON.stringify([record]))).toEqual([{ query: 'q', result: 'r', error: '', success: true }])
  })

  it('reads a failed record, and nothing from a value that is none', () => {
    expect(clineOperations([{ query: 'q', result: '', error: 'e', success: false }])).toEqual([{ query: 'q', result: '', error: 'e', success: false }])
    expect(clineOperations('plain words')).toEqual([])
    expect(clineOperations({ text: 'x' })).toEqual([])
    expect(clineOperations(undefined)).toEqual([])
  })

  // Cline states `success: false` on a failure alone, so only that value fails a record.
  it('reads a record that states no success flag as a success', () => {
    expect(clineOperations([{ query: 'q', result: 'r' }, { query: 'q2', result: 'r2', success: 'no' }]).map(operation => operation.success)).toEqual([true, true])
  })

  it('skips an entry of the list that is not a record, and reads a field of the wrong type as empty', () => {
    expect(clineOperations(['text', null, { query: 3, result: ['r'], error: {} }])).toEqual([{ query: '', result: '', error: '', success: true }])
  })
})

describe('outputText', () => {
  it('keeps a string, and writes anything else as JSON', () => {
    expect(outputText('x')).toBe('x')
    expect(outputText({ a: 1 })).toBe('{\n  "a": 1\n}')
    expect(outputText(null)).toBe('')
    expect(outputText(undefined)).toBe('')
  })

  // `0`, `false` and `''` are results. They must not read as an absent one.
  it('writes a falsy result as its own text', () => {
    expect(outputText(0)).toBe('0')
    expect(outputText(false)).toBe('false')
    expect(outputText('')).toBe('')
  })

  it('writes a value that JSON cannot state as its string', () => {
    const cycle: Record<string, unknown> = {}
    cycle.self = cycle
    expect(outputText(cycle)).toBe('[object Object]')
    expect(outputText(10n)).toBe('10')
  })
})

describe('parseJSON', () => {
  it('answers undefined for text that is not JSON', () => {
    expect(parseJSON('[1]')).toEqual([1])
    expect(parseJSON('nope')).toBeUndefined()
  })
})

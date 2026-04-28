import type { StructuredPatchHunk } from '../../../diff'
import { describe, expect, it } from 'vitest'
import {
  claudeFileEditFromToolUseInput,
  claudeFileEditFromToolUseResult,
  isClaudeFileEditTool,
} from './fileEdit'

describe('isClaudeFileEditTool', () => {
  it('recognizes Edit and Write only', () => {
    expect(isClaudeFileEditTool('Edit')).toBe(true)
    expect(isClaudeFileEditTool('Write')).toBe(true)
    expect(isClaudeFileEditTool('MultiEdit')).toBe(false)
    expect(isClaudeFileEditTool('Bash')).toBe(false)
    expect(isClaudeFileEditTool('')).toBe(false)
  })
})

describe('claudeFileEditFromToolUseInput', () => {
  it('extracts an Edit input', () => {
    const result = claudeFileEditFromToolUseInput('Edit', {
      file_path: '/tmp/a.ts',
      old_string: 'foo',
      new_string: 'bar',
    })
    expect(result).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'foo',
      newStr: 'bar',
    })
  })

  it('defaults missing Edit fields to empty strings', () => {
    expect(claudeFileEditFromToolUseInput('Edit', {})).toEqual({
      filePath: '',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
  })

  it('extracts a Write input as oldStr=empty, newStr=content', () => {
    expect(claudeFileEditFromToolUseInput('Write', {
      file_path: '/tmp/b.ts',
      content: 'package main\n',
    })).toEqual({
      filePath: '/tmp/b.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: 'package main\n',
    })
  })

  it('returns null for non-Edit/Write tool names', () => {
    expect(claudeFileEditFromToolUseInput('Bash', { command: 'ls' })).toBeNull()
    expect(claudeFileEditFromToolUseInput('Read', { file_path: '/x' })).toBeNull()
    expect(claudeFileEditFromToolUseInput('', {})).toBeNull()
  })

  it('tolerates null/undefined input', () => {
    expect(claudeFileEditFromToolUseInput('Edit', null)).toEqual({
      filePath: '',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
    expect(claudeFileEditFromToolUseInput('Write', undefined)).toEqual({
      filePath: '',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
  })
})

describe('claudeFileEditFromToolUseResult', () => {
  const PATCH: StructuredPatchHunk[] = [
    { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] },
  ]

  it('returns null for null/undefined input', () => {
    expect(claudeFileEditFromToolUseResult(null)).toBeNull()
    expect(claudeFileEditFromToolUseResult(undefined)).toBeNull()
  })

  it('extracts structuredPatch + filePath + originalFile', () => {
    expect(claudeFileEditFromToolUseResult({
      filePath: '/tmp/a.ts',
      structuredPatch: PATCH,
      originalFile: 'full original\n',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: PATCH,
      oldStr: '',
      newStr: '',
      originalFile: 'full original\n',
    })
  })

  it('extracts oldString/newString fallback when no structuredPatch is present', () => {
    expect(claudeFileEditFromToolUseResult({
      filePath: '/tmp/a.ts',
      oldString: 'pre',
      newString: 'post',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'pre',
      newStr: 'post',
      originalFile: undefined,
    })
  })

  it('returns null when the payload carries no edit-related fields at all', () => {
    expect(claudeFileEditFromToolUseResult({})).toBeNull()
    expect(claudeFileEditFromToolUseResult({ type: 'create' })).toBeNull()
  })

  it('still returns a source when only filePath is present (so the picker can display it)', () => {
    expect(claudeFileEditFromToolUseResult({ filePath: '/tmp/a.ts' })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
      originalFile: undefined,
    })
  })

  it('ignores non-array structuredPatch', () => {
    expect(claudeFileEditFromToolUseResult({
      filePath: '/tmp/a.ts',
      structuredPatch: 'not-an-array',
      oldString: 'x',
      newString: 'y',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'x',
      newStr: 'y',
      originalFile: undefined,
    })
  })
})

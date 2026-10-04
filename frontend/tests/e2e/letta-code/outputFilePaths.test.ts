import { join } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { lettaOutputFilePath } from './outputFilePaths'

describe('lettaOutputFilePath', () => {
  const home = process.platform === 'win32' ? 'C:\\private\\letta' : '/private/letta'
  const workingDir = process.platform === 'win32' ? 'C:\\private\\project space\\repo' : '/private/project space/repo'
  const path = join(home, '.letta', 'projects', process.platform === 'win32' ? 'C_private_project_space_repo' : 'private_project_space_repo', 'agent-tools', 'bash-11111111-2222-3333-4444-555555555555.txt')
  const text = `head\n\n[Output truncated: showing 2,000 of 500,000 characters.]\n[Full output written to: ${path}]`

  it('reads the complete project overflow file after foreground log removal', () => {
    expect(lettaOutputFilePath(text, home, workingDir)).toBe(path)
  })

  it.each(['', 'head', `${text}\n${text}`, text.replace('Full output written to', 'Output file'), text.replace('[Output truncated: showing 2,000 of 500,000 characters.]', '')])('rejects a different or repeated native reference %j', (value) => {
    expect(() => lettaOutputFilePath(value, home, workingDir)).toThrow('reference')
  })

  it('rejects a different profile, project, tool, or relative path', () => {
    expect(() => lettaOutputFilePath(text, join(home, 'other'), workingDir)).toThrow('project')
    expect(() => lettaOutputFilePath(text, home, join(workingDir, 'other'))).toThrow('project')
    expect(() => lettaOutputFilePath(text.replace('bash-', 'read-'), home, workingDir)).toThrow('project')
    expect(() => lettaOutputFilePath(text.replace(path, 'agent-tools/bash-11111111-2222-3333-4444-555555555555.txt'), home, workingDir)).toThrow('project')
    expect(() => lettaOutputFilePath(text, '', workingDir)).toThrow('absolute')
    expect(() => lettaOutputFilePath(text, home, 'repo')).toThrow('absolute')
  })
})

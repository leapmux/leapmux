import { describe, expect, it } from 'vitest'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { ZCODE_AGENT } from './scenarios'

describe('ZCODE_AGENT', () => {
  // ZCode reads the nearest AGENTS.md from its working directory up to the first directory that holds `.git`, and up
  // to the root of the file system when none does. No setting moves that limit. The run root above every working
  // directory holds a sentinel AGENTS.md (`../helpers/ancestorInstructions.ts`).
  it('opens in the root of a git repository of its own, where ZCode stops its search for AGENTS.md', () => {
    expect(ZCODE_AGENT.workingDir).toBe(gitRepositoryWorkingDir)
  })
})

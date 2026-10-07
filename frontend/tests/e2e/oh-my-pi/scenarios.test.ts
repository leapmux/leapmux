import { describe, expect, it } from 'vitest'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { OH_MY_PI_AGENT } from './scenarios'

describe('OH_MY_PI_AGENT', () => {
  // omp reads AGENTS.md, CLAUDE.md, the nearest .omp/ and each .agent/ and .agents/ from its working directory up to the
  // first directory that holds `.git`, and up to the root of the file system when none does. Its setting
  // `disabledProviders` turns a discovery off for every directory, the working directory too. The run root above every
  // working directory holds the sentinel files (`../helpers/ancestorInstructions.ts`).
  it('opens in the root of a git repository of its own, where omp stops its search for context files', () => {
    expect(OH_MY_PI_AGENT.workingDir).toBe(gitRepositoryWorkingDir)
  })
})

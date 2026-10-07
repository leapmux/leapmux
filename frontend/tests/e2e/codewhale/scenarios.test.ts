import { describe, expect, it } from 'vitest'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { CODEWHALE_AGENT } from './scenarios'

describe('CODEWHALE_AGENT', () => {
  // Codewhale reads the nearest .codewhale/constitution.json from its working directory up to the root of its git
  // repository, and up to the root of the file system when no repository holds the working directory. No setting turns
  // that off. The run root above every working directory holds a sentinel constitution
  // (`../helpers/ancestorInstructions.ts`).
  it('opens in the root of a git repository of its own, where Codewhale stops its search for a constitution', () => {
    expect(CODEWHALE_AGENT.workingDir).toBe(gitRepositoryWorkingDir)
  })
})

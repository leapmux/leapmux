import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative, SOURCE_EXTENSIONS } from '~/test-support/sourceTree'

const allowedModules = new Set(['src/lib/noise.ts', 'src/lib/noise-hybrid.ts'])
const forbiddenTokens = ['WithEntropy', 'NoiseEntropy', 'systemEntropy']

describe('Noise entropy injection', () => {
  it('appears only in the Noise modules, test files, and test support', () => {
    const root = join(frontendRoot, 'src')
    const files = collectFiles(root, {
      matches: name => SOURCE_EXTENSIONS.some(extension => name.endsWith(extension)),
    })
    expect(files.length).toBeGreaterThan(0)
    const offenders: string[] = []
    for (const file of files) {
      const name = posixRelative(frontendRoot, file)
      if (allowedModules.has(name) || /(?:\.test|\.spec)\.[jt]sx?$/.test(name) || name.includes('/test-support/'))
        continue
      const source = readFileSync(file, 'utf8')
      if (forbiddenTokens.some(token => source.includes(token)))
        offenders.push(name)
    }
    expect(offenders).toEqual([])
  })
})

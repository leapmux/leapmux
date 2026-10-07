import { join, matchesGlob } from 'node:path'
import { describe, expect, it } from 'vitest'
import { AMP_IGNORED_GUIDANCE_FILES, createAmpEnvironment } from './ampEnvironment'
import { RUN_ROOT_PREFIX } from './runRoot'

describe('createAmpEnvironment', () => {
  it('points Amp and its actor gateway at the given origin with the given key, and empties each inherited setting', () => {
    expect(createAmpEnvironment({ origin: 'http://127.0.0.1:4567', modelKey: 'unit-key' })).toEqual({
      AMP_URL: 'http://127.0.0.1:4567',
      AMP_API_KEY: 'unit-key',
      RIVET_PUBLIC_ENDPOINT: 'http://127.0.0.1:4567/actors',
      RIVET_POOL: '',
      AMP_SETTINGS_FILE: '',
      AMP_SKIP_UPDATE_CHECK: '1',
      AMP_REMOTE_CONTROL_TERMINAL: '0',
      AMP_IGNORE_GUIDANCE_FILES: AMP_IGNORED_GUIDANCE_FILES,
    })
  })
})

// Node's glob matcher stands in for Amp's here. A native probe of Amp 0.0.1791074829 showed the same split for a glob of
// this form: the file of the run root went, and the file of the working directory stayed.
describe('AMP_IGNORED_GUIDANCE_FILES', () => {
  const runRoot = `/private/tmp/${RUN_ROOT_PREFIX}AbC123`

  it('matches each sentinel file directly in a run root, whatever the parent of the run root', () => {
    for (const parent of ['/private/tmp', '/home/ci/runs']) {
      const root = `${parent}/${RUN_ROOT_PREFIX}Xy9z01`
      for (const name of ['AGENTS.md', 'AGENT.md', 'CLAUDE.md', 'CONTEXT.md'])
        expect(matchesGlob(join(root, name), AMP_IGNORED_GUIDANCE_FILES), join(root, name)).toBe(true)
    }
  })

  it('matches no file of a working directory below the run root', () => {
    for (const path of [join(runRoot, '1', 'work', 'AGENTS.md'), join(runRoot, 'AGENTS', 'AGENTS.md'), join(runRoot, '1', 'AGENTS.md')])
      expect(matchesGlob(path, AMP_IGNORED_GUIDANCE_FILES), path).toBe(false)
  })

  it('matches no file outside a run root, which the guard of the Amp surface refuses instead', () => {
    for (const path of ['/private/tmp/AGENTS.md', '/AGENTS.md', '/private/tmp/leapmux-other/AGENTS.md'])
      expect(matchesGlob(path, AMP_IGNORED_GUIDANCE_FILES), path).toBe(false)
  })
})

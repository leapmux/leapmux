import { describe, expect, it } from 'vitest'
import { createAmpEnvironment } from './ampEnvironment'

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
    })
  })
})

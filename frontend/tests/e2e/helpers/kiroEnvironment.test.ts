import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createKiroEnvironment } from './kiroEnvironment'

let runDirectory: string
const options = () => ({ runDirectory, homeDir: join(runDirectory, 'home'), origin: 'http://127.0.0.1:4567', apiKey: 'ksk_unit' })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'kiro-environment-test-'))
})

afterEach(() => rmSync(runDirectory, { recursive: true, force: true }))

describe('createKiroEnvironment', () => {
  it('pins every service of its settings to the given origin', () => {
    const env = createKiroEnvironment(options())
    const service = { endpoint: 'http://127.0.0.1:4567', region: 'us-east-1' }
    expect(JSON.parse(readFileSync(join(env.KIRO_HOME!, 'settings', 'cli.json'), 'utf8'))).toEqual({
      'api.krs.service': service,
      'api.cps.service': service,
      'api.codewhisperer.service': service,
      'api.q.service': 'http://127.0.0.1:4567',
      'api.kiroauth.service': 'http://127.0.0.1:4567',
      'telemetry.enabled': false,
      'app.disableAutoupdates': true,
    })
  })

  it('sends the given key, keeps its data in the run, and points its remote endpoints at the origin', () => {
    const env = createKiroEnvironment(options())
    expect(env).toMatchObject({
      KIRO_HOME: join(runDirectory, 'home', '.kiro'),
      KIRO_DATA_DIR: join(runDirectory, 'kiro-data'),
      KIRO_API_KEY: 'ksk_unit',
      KIRO_REMOTE_SESSIONS_ENDPOINT: 'http://127.0.0.1:4567',
      CLOUD_CONFIG_ENDPOINT: 'http://127.0.0.1:4567',
      KIRO_DISABLE_SESSION_TITLE_LLM: 'true',
      KIRO_NO_AUTO_UPDATE: '1',
    })
    expect(statSync(env.KIRO_DATA_DIR!).isDirectory()).toBe(true)
  })

  it('lets no AWS profile or credential of the developer reach the agent', () => {
    expect(createKiroEnvironment(options())).toMatchObject({
      AWS_PROFILE: 'default',
      AWS_CONFIG_FILE: join(runDirectory, 'home', '.aws', 'config'),
      AWS_SHARED_CREDENTIALS_FILE: join(runDirectory, 'home', '.aws', 'credentials'),
      AWS_ACCESS_KEY_ID: '',
      AWS_SECRET_ACCESS_KEY: '',
      AWS_SESSION_TOKEN: '',
    })
  })
})

import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { KIMI_CODE_SERVICE_PATH } from './kimiCodeSurface'
import { createKimiEnvironment, KIMI_ACCOUNT_PROVIDER, kimiAccountCredentialKey } from './kimiEnvironment'
import { mcpProbeServer } from './mcpProbeServer'
import { privateFileMode } from './privateConfigFile'

let homeDir: string
const echoServer = mcpProbeServer('echo_probe', '/srv/echo.mjs')
function options() {
  return {
    homeDir,
    baseURL: 'http://127.0.0.1:4567/v1',
    providerID: 'unit-provider',
    thinking: { alias: 'unit-provider/thinking', model: 'thinking-model' },
    plain: { alias: 'unit-provider/plain', model: 'plain-model' },
    alternateThinking: { alias: 'unit-provider/wide', model: 'wide-model' },
    mcpEchoServer: echoServer,
    account: {
      origin: 'http://127.0.0.1:4567',
      accessToken: 'unit-account-token',
      model: { alias: 'kimi-code/account-model', model: 'account-model' },
    },
  }
}

/** The text of the `[models."<alias>"]` table, up to the next table or the end. */
function modelTable(config: string, alias: string): string {
  const start = config.indexOf(`[models."${alias}"]`)
  if (start < 0)
    throw new Error(`The configuration has no model ${alias}.`)
  const next = config.indexOf('\n[', start + 1)
  return config.slice(start, next < 0
    ? undefined
    : next)
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  homeDir = mkdtempSync(join(scratch, 'kimi-environment-test-'))
})

afterEach(() => rmSync(homeDir, { recursive: true, force: true }))

describe('kimiAccountCredentialKey', () => {
  // Kimi Code 2.3 hashes the JSON of the sign-in host and the service root, in that order, each without a trailing
  // slash, and keeps the first 16 hex digits.
  it('derives the key that Kimi Code reads the token from', () => {
    const digest = createHash('sha256').update('{"oauthHost":"https://auth.kimi.com","baseUrl":"http://127.0.0.1:9/coding/v1"}').digest('hex')
    expect(kimiAccountCredentialKey('https://auth.kimi.com', 'http://127.0.0.1:9/coding/v1')).toBe(`kimi-code-env-${digest.slice(0, 16)}`)
  })

  it('reads a host and a root with trailing slashes or spaces as without them', () => {
    const key = kimiAccountCredentialKey('https://auth.kimi.com', 'http://127.0.0.1:9/coding/v1')
    expect(kimiAccountCredentialKey(' https://auth.kimi.com// ', 'http://127.0.0.1:9/coding/v1/')).toBe(key)
  })

  it('gives another service root another key', () => {
    const key = kimiAccountCredentialKey('https://auth.kimi.com', 'http://127.0.0.1:9/coding/v1')
    expect(key).not.toBe(kimiAccountCredentialKey('https://auth.kimi.com', 'http://127.0.0.1:10/coding/v1'))
  })
})

describe('createKimiEnvironment', () => {
  it('stores the token of the mocked account under the key of its service, and never lets it expire', () => {
    const env = createKimiEnvironment(options())
    const file = join(env.KIMI_CODE_HOME!, 'credentials', `${kimiAccountCredentialKey(env.KIMI_CODE_OAUTH_HOST!, env.KIMI_CODE_BASE_URL!)}.json`)
    expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual({
      access_token: 'unit-account-token',
      refresh_token: 'unit-account-token',
      expires_at: 0,
      expires_in: 0,
      token_type: 'Bearer',
      scope: '',
    })
    if (process.platform !== 'win32')
      expect(statSync(file).mode & 0o777).toBe(privateFileMode)
  })

  it('lists the model of the account under the account\'s provider, which serves it from the mock', () => {
    const config = readFileSync(join(createKimiEnvironment(options()).KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain(`[providers."${KIMI_ACCOUNT_PROVIDER}"]\ntype = "openai"\nbase_url = "http://127.0.0.1:4567/v1"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"`)
    const account = modelTable(config, 'kimi-code/account-model')
    expect(account).toContain(`provider = "${KIMI_ACCOUNT_PROVIDER}"\nmodel = "account-model"`)
    expect(account).toContain('capabilities = ["tool_use"]')
    expect(config).toContain('default_model = "unit-provider/thinking"')
  })

  it('writes the blocking hook for the dedicated marker only', () => {
    const env = createKimiEnvironment(options())
    const config = readFileSync(join(env.KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('[[hooks]]\nevent = "UserPromptSubmit"\nmatcher = "LEAPMUX-HOOK-BLOCK"')
    expect(config.match(/\[\[hooks\]\]/g)).toHaveLength(1)
    const script = join(homeDir, 'kimi-block-hook.sh')
    expect(config).toContain(`command = ${JSON.stringify(`'${script}'`)}`)
    expect(readFileSync(script, 'utf8')).toContain('LeapMux test hook blocked this prompt')
    expect(readFileSync(script, 'utf8')).toContain('exit 2')
    if (process.platform !== 'win32')
      expect(statSync(script).mode & 0o777).toBe(0o700)
  })

  it('maps each alias onto its model, and selects the thinking alias by default', () => {
    const env = createKimiEnvironment(options())
    const config = readFileSync(join(env.KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('default_model = "unit-provider/thinking"')
    expect(config).toContain('[providers.unit-provider]\ntype = "openai"\nbase_url = "http://127.0.0.1:4567/v1"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"')
    expect(config).toContain('[models."unit-provider/thinking"]\nprovider = "unit-provider"\nmodel = "thinking-model"')
    expect(config).toContain('[models."unit-provider/plain"]\nprovider = "unit-provider"\nmodel = "plain-model"')
    expect(config).toContain('[models."unit-provider/wide"]\nprovider = "unit-provider"\nmodel = "wide-model"')
    expect(config).toContain('auto_session_title = false')
  })

  // A switch that keeps a level must differ from a switch that takes the default of the new model.
  it('gives the two thinking aliases one ladder with different defaults, and the plain alias no thinking', () => {
    const config = readFileSync(join(createKimiEnvironment(options()).KIMI_CODE_HOME!, 'config.toml'), 'utf8')
    const thinking = modelTable(config, 'unit-provider/thinking')
    const alternateThinking = modelTable(config, 'unit-provider/wide')
    const plain = modelTable(config, 'unit-provider/plain')
    for (const table of [thinking, alternateThinking]) {
      expect(table).toContain('capabilities = ["tool_use", "thinking"')
      expect(table).toContain('support_efforts = ["low", "medium", "high"]')
    }
    expect(thinking).toContain('default_effort = "high"')
    expect(alternateThinking).toContain('default_effort = "medium"')
    expect(plain).toContain('capabilities = ["tool_use"]')
    expect(plain).not.toContain('support_efforts')
  })

  it('lists the echo server, and turns off telemetry and the update', () => {
    const env = createKimiEnvironment(options())
    expect(env).toEqual({
      KIMI_CODE_HOME: join(homeDir, '.kimi-code'),
      KIMI_DISABLE_TELEMETRY: '1',
      KIMI_CODE_NO_AUTO_UPDATE: '1',
      KIMI_CODE_TRUST_WORKSPACE: '1',
      KIMI_CODE_BASE_URL: `http://127.0.0.1:4567${KIMI_CODE_SERVICE_PATH}`,
      KIMI_CODE_OAUTH_HOST: 'http://127.0.0.1:4567/kimi/oauth',
    })
    expect(JSON.parse(readFileSync(join(env.KIMI_CODE_HOME!, 'mcp.json'), 'utf8'))).toEqual({
      mcpServers: {
        echo_probe: {
          command: echoServer.command,
          args: ['/srv/echo.mjs'],
        },
      },
    })
  })
})

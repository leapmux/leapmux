import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createGrokEnvironment } from './grokEnvironment'

let runDirectory: string
const options = () => ({ runDirectory, homeDir: join(runDirectory, 'home'), baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', modelID: 'unit-model', alternateModelID: 'unit-alt', reasoningModelID: 'unit-reasoning' })

/** The text of the `[model."<id>"]` table and its effort tables, up to the next model table or the end. */
function modelSection(config: string, id: string): string {
  const start = config.indexOf(`[model."${id}"]`)
  if (start < 0)
    throw new Error(`The configuration has no model ${id}.`)
  const next = config.indexOf('\n[model."', start + 1)
  return config.slice(start, next < 0 ? undefined : next)
}

/** The effort IDs of one model section, and the ID of its default. */
function effortLadder(section: string): { levels: string[], defaultLevel: string | undefined } {
  const tables = [...section.matchAll(/\[\[model\."[^"]+"\.reasoning_efforts\]\]\nid = "(\w+)"\nvalue = "\w+"\nlabel = "\w+"\ndefault = (true|false)/g)]
  return { levels: tables.map(table => table[1]!), defaultLevel: tables.find(table => table[2] === 'true')?.[1] }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'grok-environment-test-'))
})

afterEach(() => rmSync(runDirectory, { recursive: true, force: true }))

describe('createGrokEnvironment', () => {
  it('keeps Grok\'s home in the isolated home and its lock slots in the run', () => {
    const env = createGrokEnvironment(options())
    expect(env.GROK_HOME).toBe(join(runDirectory, 'home', '.grok'))
    expect(env.GROK_FILE_LOCK_SLOT_DIR).toBe(join(runDirectory, 'grok-lock-slots'))
    expect(statSync(env.GROK_FILE_LOCK_SLOT_DIR!).isDirectory()).toBe(true)
  })

  it('pins the given models to the mock and hides the built-in ones', () => {
    const config = readFileSync(join(createGrokEnvironment(options()).GROK_HOME!, 'config.toml'), 'utf8')
    expect(config).toContain('[models]\ndefault = "unit-model"\nsession_summary = "unit-model"')
    expect(config).toContain('[model."unit-model"]\nmodel = "unit-model"\nbase_url = "http://127.0.0.1:4567/v1"\nname = "Grok E2E"\napi_key = "unit-key"')
    expect(config).toContain('[model."unit-alt"]\nmodel = "unit-alt"\nbase_url = "http://127.0.0.1:4567/v1"')
    expect(config).toContain('[model."unit-reasoning"]\nmodel = "unit-reasoning"\nbase_url = "http://127.0.0.1:4567/v1"\nname = "Grok E2E Reasoning"')
    expect(config).toContain('[model."grok-4.6"]\nhidden = true')
    expect(config).toMatch(/^\[cli\]\nauto_update = false$/m)
  })

  // A model switch that keeps a level must differ from a switch that takes the default of the new model.
  it('gives the default and the reasoning model one ladder with different defaults, and the alternate model none', () => {
    const config = readFileSync(join(createGrokEnvironment(options()).GROK_HOME!, 'config.toml'), 'utf8')
    const primary = modelSection(config, 'unit-model')
    const reasoning = modelSection(config, 'unit-reasoning')
    expect(primary).toContain('supports_reasoning_effort = true\nreasoning_effort = "medium"')
    expect(effortLadder(primary)).toEqual({ levels: ['low', 'medium', 'high'], defaultLevel: 'medium' })
    expect(reasoning).toContain('supports_reasoning_effort = true\nreasoning_effort = "high"')
    expect(effortLadder(reasoning)).toEqual({ levels: ['low', 'medium', 'high'], defaultLevel: 'high' })
    const alternate = modelSection(config, 'unit-alt')
    expect(alternate).not.toContain('reasoning')
  })

  it('turns off every model call that no test scripts', () => {
    expect(createGrokEnvironment(options())).toMatchObject({
      GROK_DISABLE_AUTOUPDATER: '1',
      GROK_TURN_SUMMARY: '0',
      GROK_TITLE_REFRESH: '0',
      GROK_SESSION_RECAP: '0',
      GROK_PROMPT_SUGGESTIONS: '0',
      GROK_MEMORY: '0',
      GROK_LOGIN_ENV: '0',
    })
  })
})

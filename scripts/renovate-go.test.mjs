import { describe, expect, it } from 'bun:test'
import { GO_TOOL_MODULES } from './go-tools.mjs'
import { renovateModule } from './renovate-engine.mjs'
import { finishGoUpgrades, prepareGoUpgrades } from './renovate-go.mjs'

const { extractPackageFile } = await renovateModule('modules/manager/gomod/extract.js')

describe('prepareGoUpgrades', () => {
  it('leaves native replacement updates intact when the source and target module paths differ', () => {
    const metadata = extractPackageFile('module example.com/tool\ngo 1.27.1\nreplace example.com/original => example.com/replacement v1.0.0\n').deps.find(dep => dep.depType === 'replace')
    expect(metadata.depName).toBe('example.com/replacement')
    const calls = []
    prepareGoUpgrades([
      {
        ...metadata,
        manager: 'gomod',
        packageFile: 'tools/sqlc/go.mod',
        newValue: 'v1.1.0',
      },
    ], {
      trusted: '/trusted',
      run: (command, args) => { calls.push({ command, args }) },
    })
    expect(calls.filter(call => call.command === 'go').map(call => call.args)).toEqual([
      ['mod', 'download', 'all'],
    ])
  })
  it('isolates tool metadata and refreshes application imports only after SQL generation', () => {
    for (const packageFile of GO_TOOL_MODULES) {
      const calls = []
      const directory = packageFile.replace('/go.mod', '')
      const options = {
        trusted: '/trusted',
        read: () => { throw new Error('Tool update read an application graph.') },
        run: (command, args, options = {}) => {
          calls.push({ command, args, options })
        },
      }
      const state = prepareGoUpgrades([
        {
          manager: 'gomod',
          packageFile,
          depName: 'example.com/tool/v1',
          newValue: 'v2.0.0',
        },
      ], options)
      for (const call of calls.filter(call => call.command === 'go')) {
        expect(call.options.cwd).toBe(directory)
        expect(call.options.env.GOWORK).toBe('off')
        expect(call.args[0]).not.toBe('work')
      }
      finishGoUpgrades(state, options)
      const rewrite = calls.find(call => call.options.input)
      expect(JSON.parse(rewrite.options.input)[0].directories).toEqual([directory])
      const isSqlc = packageFile === 'tools/sqlc/go.mod'
      expect(calls.filter(call => call.command === 'go').map(call => call.args[1])).toEqual(isSqlc
        ? [
            'edit',
            'download',
            'tidy',
            'tidy',
            'tidy',
            'sync',
          ]
        : ['edit', 'download', 'tidy'])
      expect(calls.filter(call => call.command === 'go' && call.args[1] === 'tidy').map(call => call.options.cwd)).toEqual(isSqlc
        ? [directory, 'backend', 'desktop/go']
        : [directory])
      expect(calls.some(call => call.command === 'task' && call.args[0] === 'prepare-backend')).toBe(packageFile === 'tools/sqlc/go.mod')
    }
  })
  it('skips both phases without Go upgrades or graph access', () => {
    const unexpected = () => {
      throw new Error('An unrelated update accessed the Go graph.')
    }
    for (const upgrades of [
      [],
      [
        { manager: 'bun', packageFile: 'frontend/package.json' },
      ],
    ]) {
      const options = {
        root: '/fixture',
        trusted: '/trusted',
        run: unexpected,
        read: unexpected,
        write: unexpected,
      }
      const state = prepareGoUpgrades(upgrades, options)
      expect(state).toEqual({ hasWorkspaceGo: false, hasSiteGo: false, toolModules: [] })
      finishGoUpgrades(state, options)
    }
  })
})

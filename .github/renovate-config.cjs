const fs = require('node:fs')
const path = require('node:path')
const process = require('node:process')

const trusted = process.env.RENOVATE_TRUSTED_ROOT
if (!trusted || !path.isAbsolute(trusted))
  throw new Error('RENOVATE_TRUSTED_ROOT must identify the trusted main checkout.')
const config = JSON.parse(fs.readFileSync(path.join(trusted, 'renovate.json'), 'utf8'))
const prepared = JSON.parse(fs.readFileSync(path.join(trusted, '.tmp/renovate-prepared.json'), 'utf8'))
const command = `bun ${JSON.stringify(path.join(trusted, 'scripts/renovate-post-upgrade.mjs'))}`
module.exports = {
  platform: 'github',
  repositories: process.env.RENOVATE_PLATFORM === 'local' ? [] : ['leapmux/leapmux'],
  autodiscover: false,
  onboarding: false,
  requireConfig: 'ignored',
  binarySource: 'global',
  allowScripts: false,
  allowPlugins: false,
  allowShellExecutorForPostUpgradeCommands: false,
  allowedCommands: [`^${RegExp.escape(command)}$`],
  executionTimeout: 50,
  customEnvVariables: { RENOVATE_TRUSTED_ROOT: trusted, GOTOOLCHAIN: 'local' },
  exposeAllEnv: false,
  baseDir: path.join(trusted, '.tmp/renovate-run'),
  ...config,
  packageRules: [...config.packageRules, ...prepared.packageRules],
  postUpgradeTasks: {
    commands: [command],
    executionMode: 'branch',
    dataFileTemplate: '[{{#each upgrades}}{"manager":{{{toJSON manager}}}{{#if branchName}},"branchName":{{{toJSON branchName}}}{{/if}}{{#if packageFile}},"packageFile":{{{toJSON packageFile}}}{{/if}}{{#if packageName}},"packageName":{{{toJSON packageName}}}{{/if}}{{#if depName}},"depName":{{{toJSON depName}}}{{/if}}{{#if newValue}},"newValue":{{{toJSON newValue}}}{{/if}}{{#if newVersion}},"newVersion":{{{toJSON newVersion}}}{{/if}}{{#if updateType}},"updateType":{{{toJSON updateType}}}{{/if}}{{#if depType}},"depType":{{{toJSON depType}}}{{/if}}{{#if isVulnerabilityAlert}},"isVulnerabilityAlert":{{{toJSON isVulnerabilityAlert}}}{{/if}} } {{#unless @last}},{{/unless}}{{/each}}]',
    fileFilters: [
      'versions.env',
      'README.md',
      'site/content/docs/getting-started/installation.md',
      'go.work',
      'backend/go.mod',
      'backend/go.sum',
      'desktop/go/go.mod',
      'desktop/go/go.sum',
      'site/go.mod',
      'site/go.sum',
      ...prepared.importFiles,
      'buf.gen.yaml',
      'frontend/package.json',
      'frontend/bun.lock',
      'desktop/rust/Cargo.toml',
      'desktop/rust/Cargo.lock',
      'NOTICE.md',
      'NOTICE.html',
    ],
  },
}

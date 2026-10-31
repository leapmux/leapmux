const fs = require('node:fs')
const path = require('node:path')
const process = require('node:process')
const { configureDetailsCapture } = require('../scripts/renovate-pr-details.mjs')

// Renovate's loader and config-validator.js both call an exported function and await its result.
module.exports = async () => {
  const trusted = process.env.RENOVATE_TRUSTED_ROOT
  if (!trusted || !path.isAbsolute(trusted))
    throw new Error('RENOVATE_TRUSTED_ROOT must identify the trusted main checkout.')
  // Self-hosted `extends` resolves after global package rules and can replace their groups.
  const { extends: globalExtends, ...config } = JSON.parse(fs.readFileSync(path.join(trusted, 'renovate.json'), 'utf8'))
  const prepared = JSON.parse(fs.readFileSync(path.join(trusted, '.tmp/renovate-prepared.json'), 'utf8'))
  const command = `bun ${JSON.stringify(path.join(trusted, 'scripts/renovate-post-upgrade.mjs'))}`
  const { prHeader, prBodyTemplate } = configureDetailsCapture(trusted, config.prBodyTemplate)
  // run-renovate.mjs writes these shims, which hold bun and cargo resolution to the release age.
  const { toolShimPaths } = await import('../scripts/renovate-shims.mjs')
  const { configureSkippedDependencies } = await import('../scripts/renovate-dashboard.mjs')
  const { dependencyDashboardFooter } = await configureSkippedDependencies({ policy: config, alerts: prepared.alerts ?? [] })
  return {
    platform: 'github',
    repositories: process.env.RENOVATE_PLATFORM === 'local'
      ? []
      : ['leapmux/leapmux'],
    autodiscover: false,
    onboarding: false,
    requireConfig: 'ignored',
    binarySource: 'global',
    allowScripts: false,
    allowPlugins: false,
    allowShellExecutorForPostUpgradeCommands: false,
    allowedCommands: [`^${RegExp.escape(command)}$`],
    executionTimeout: 50,
    // Renovate otherwise exits with 0 after a repository run ends in an error, such
    // as a network reset (external-host-error), so the workflow job would pass.
    exitCodeForErrors: true,
    customEnvVariables: {
      RENOVATE_TRUSTED_ROOT: trusted,
      GOTOOLCHAIN: 'local',
      PATH: `${toolShimPaths(trusted).bin}${path.delimiter}${process.env.PATH}`,
    },
    exposeAllEnv: false,
    baseDir: path.join(trusted, '.tmp/renovate-run'),
    globalExtends,
    // Renovate re-extracts a package file after an update to check its lockfile result,
    // which the bun manager cannot do. The tool shims and check-dependency-age enforce
    // the release age of lockfile versions instead.
    logLevelRemap: [
      { matchMessage: 'Could not re-extract the packageFile after updating it', newLogLevel: 'debug' },
    ],
    ...config,
    prHeader,
    prBodyTemplate,
    dependencyDashboardFooter,
    // Filter additive manager defaults before security overrides can enable an update.
    ...(prepared.ignorePaths
      ? { ignorePaths: prepared.ignorePaths }
      : {}),
    packageRules: [
      ...config.packageRules,
      ...prepared.packageRules,
    ],
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
        'site/hugo.yaml',
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
}

// Tool modules stay outside go.work so each tool selects its own dependencies.
export const GO_TOOL_NAMES = Object.freeze(['golangci-lint', 'sqlc', 'gotestsum'])
export const GO_TOOL_MODULES = Object.freeze(GO_TOOL_NAMES.map(name => `tools/${name}/go.mod`))
export const GO_WORKSPACE_MODULES = Object.freeze(['backend/go.mod', 'desktop/go/go.mod'])

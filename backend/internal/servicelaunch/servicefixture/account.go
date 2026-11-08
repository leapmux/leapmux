// Package servicefixture defines the private account fixture for native service tests.
package servicefixture

// Account is the SERVICE_ACCOUNT_FILE YAML record. It contains no password.
type Account struct {
	AccountID             string  `yaml:"account_id"`
	AccountName           string  `yaml:"account_name"`
	CredentialReference   *string `yaml:"credential_reference"`
	DisposableEnvironment bool    `yaml:"disposable_environment"`
}

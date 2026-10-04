//go:build !integration

package configitems

// ensureIntegrationSchema is a no-op for the unit run, which has no services.
// See setup_integration_test.go.
func ensureIntegrationSchema() {}

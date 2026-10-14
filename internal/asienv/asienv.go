// Package asienv reads the ASI service base URLs from the environment.
// Each ASI_*_BASE_URL is required. The process does not invent hosts or ports.
package asienv

import (
	"fmt"
	"os"
	"strings"
)

var baseURLEnvVars = []string{
	"ASI_CONFIG_BASE_URL",
	"ASI_AUTH_BASE_URL",
	"ASI_REPORTING_BASE_URL",
	"ASI_RESERVATION_BASE_URL",
}

// Apply returns the effective ASI base URLs. Startup fails if any is unset.
func Apply() ([]string, error) {
	lines := make([]string, 0, len(baseURLEnvVars))
	var missing []string
	for _, key := range baseURLEnvVars {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			missing = append(missing, key)
			continue
		}
		lines = append(lines, key+"="+value)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ASI base URLs are required: set %s", strings.Join(missing, ", "))
	}
	return lines, nil
}

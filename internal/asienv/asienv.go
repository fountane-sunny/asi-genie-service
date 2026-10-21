// Every ASI_*_BASE_URL is required. Hosts and ports are never invented.
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

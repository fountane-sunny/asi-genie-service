package asienv

import (
	"strings"
	"testing"
)

func TestApplyRequiresEveryBaseURL(t *testing.T) {
	t.Setenv("ASI_CONFIG_BASE_URL", "http://config.example/v1")
	t.Setenv("ASI_AUTH_BASE_URL", "http://auth.example/v1/auth")
	t.Setenv("ASI_REPORTING_BASE_URL", "http://reporting.example/v1/reporting")
	t.Setenv("ASI_RESERVATION_BASE_URL", "")

	_, err := Apply()
	if err == nil || !strings.Contains(err.Error(), "ASI_RESERVATION_BASE_URL") {
		t.Fatalf("missing reservation URL: err=%v", err)
	}
}

func TestApplyReturnsEnvValues(t *testing.T) {
	t.Setenv("ASI_CONFIG_BASE_URL", "http://config.example/v1")
	t.Setenv("ASI_AUTH_BASE_URL", "http://auth.example/v1/auth")
	t.Setenv("ASI_REPORTING_BASE_URL", "http://reporting.example/v1/reporting")
	t.Setenv("ASI_RESERVATION_BASE_URL", "http://reservation.example/api/v1")

	lines, err := Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"ASI_CONFIG_BASE_URL=http://config.example/v1",
		"ASI_AUTH_BASE_URL=http://auth.example/v1/auth",
		"ASI_REPORTING_BASE_URL=http://reporting.example/v1/reporting",
		"ASI_RESERVATION_BASE_URL=http://reservation.example/api/v1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

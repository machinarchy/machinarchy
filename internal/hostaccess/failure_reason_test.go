package hostaccess

import (
	"fmt"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func TestDeniedTargetIsNamedAsPolicyNotNetwork(t *testing.T) {
	denied := fmt.Errorf("dial: %w", domain.ErrTargetNotAllowed)
	if got := failureReason(denied); got != "TargetNotAllowed" {
		t.Fatalf("reason = %q, want TargetNotAllowed", got)
	}
	message := failureMessage(denied)
	for _, name := range []string{"MACHINARCHY_SSH_ALLOW_LOOPBACK", "MACHINARCHY_SSH_ALLOWED_PORTS"} {
		if !strings.Contains(message, name) {
			t.Fatalf("message %q does not name %s", message, name)
		}
	}
}

func TestOtherFailuresKeepTheirReasonAndGetNoPolicyMessage(t *testing.T) {
	if got := failureReason(ErrConnection); got != "ConnectionFailed" {
		t.Fatalf("reason = %q, want ConnectionFailed", got)
	}
	if got := failureMessage(ErrConnection); got != "" {
		t.Fatalf("message = %q, want empty", got)
	}
}

package bootstrap

import (
	"fmt"

	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/mailer"
	"github.com/machinarchy/machinarchy/internal/service"
	"github.com/machinarchy/machinarchy/internal/store"
)

// newInvitationService wires email invitations. Without MACHINARCHY_SMTP_HOST
// the service still exists (listing, revoking, accepting), and inviting
// answers "email delivery is not configured".
func newInvitationService(stores *store.Stores, cfg config.Config) (domain.InvitationService, error) {
	var sender domain.Mailer
	if cfg.Mail.Enabled() {
		smtp, err := mailer.NewSMTP(cfg.Mail)
		if err != nil {
			return nil, fmt.Errorf("configure invitation mail: %w", err)
		}
		sender = smtp
	}
	svc, err := service.NewInvitationService(stores.Invitations, stores.Users, sender, cfg.Mail.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("configure invitation service: %w", err)
	}
	return svc, nil
}

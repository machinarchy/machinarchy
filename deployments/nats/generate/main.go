// Command generate creates a NATS operator-mode setup for machinarchy and its
// agents (AUDIT M-10): an operator, a system account, the MACHINARCHY account
// with JetStream and a signing key, and two users:
//
//   - controller: the agent gateway, with agentgateway.ControllerPermissions;
//   - bootstrap: the shared credential agents use before enrolment, with
//     agentv1.BootstrapPermissions.
//
// Enrolled agents get their own short-lived user JWTs from the gateway, signed
// with the account signing key, so they need no file from this tool beyond
// bootstrap.creds.
//
//	go run ./deployments/nats/generate -out ./nats-auth
//
// Teams that manage NATS with nsc can create the same accounts and users with
// the permissions listed in docs/operations.md instead.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
)

func main() {
	out := flag.String("out", "", "directory to create (must not exist)")
	account := flag.String("account", "MACHINARCHY", "name of the application account")
	flag.Parse()
	if err := run(*out, *account); err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}

type setup struct {
	dir   string
	files map[string][]byte
}

func run(out, accountName string) error {
	if out == "" {
		return errors.New("-out is required")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite keys", out)
	}
	s := &setup{dir: out, files: map[string][]byte{}}

	operator, err := nkeys.CreateOperator()
	if err != nil {
		return err
	}
	operatorPublic, err := operator.PublicKey()
	if err != nil {
		return err
	}
	system, systemJWT, err := s.account(operator, "SYS", false, nil)
	if err != nil {
		return err
	}
	signer, err := nkeys.CreateAccount()
	if err != nil {
		return err
	}
	accountKey, accountJWT, err := s.account(operator, accountName, true, signer)
	if err != nil {
		return err
	}
	operatorClaims := jwt.NewOperatorClaims(operatorPublic)
	operatorClaims.Name = "machinarchy"
	operatorClaims.SystemAccount = system
	operatorJWT, err := operatorClaims.Encode(operator)
	if err != nil {
		return err
	}

	controller := agentgateway.ControllerPermissions()
	bootstrap := agentv1.BootstrapPermissions()
	if err := s.user("controller", accountKey, signer, controller, true); err != nil {
		return err
	}
	if err := s.user("bootstrap", accountKey, signer, bootstrap, false); err != nil {
		return err
	}

	for name, key := range map[string]nkeys.KeyPair{"keys/operator.nk": operator, "keys/account-signing.nk": signer} {
		seed, err := key.Seed()
		if err != nil {
			return err
		}
		s.files[name] = append(seed, '\n')
	}
	s.files["nats-server.conf"] = []byte(serverConfig(operatorJWT, system, map[string]string{system: systemJWT, accountKey: accountJWT}))
	s.files["machinarchy.env"] = []byte(strings.Join([]string{
		"# machinarchy (agent gateway). Paths are relative to this directory; make them absolute.",
		"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT=" + accountKey,
		"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE=keys/account-signing.nk",
		"MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE=controller.creds",
		"",
	}, "\n"))
	s.files["agent.env"] = []byte("# every agent host (only until it has enrolled, and to recover an expired JWT)\n" +
		"MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=bootstrap.creds\n")
	if err := s.write(); err != nil {
		return err
	}
	fmt.Printf("Wrote %s. Account %s.\nKeep keys/operator.nk offline; keys/account-signing.nk goes to machinarchy only.\n", out, accountKey)
	return nil
}

func (s *setup) account(operator nkeys.KeyPair, name string, jetStream bool, signer nkeys.KeyPair) (string, string, error) {
	key, err := nkeys.CreateAccount()
	if err != nil {
		return "", "", err
	}
	public, err := key.PublicKey()
	if err != nil {
		return "", "", err
	}
	claims := jwt.NewAccountClaims(public)
	claims.Name = name
	if signer != nil {
		signingPublic, err := signer.PublicKey()
		if err != nil {
			return "", "", err
		}
		claims.SigningKeys.Add(signingPublic)
	}
	if jetStream {
		claims.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
	}
	encoded, err := claims.Encode(operator)
	if err != nil {
		return "", "", err
	}
	seed, err := key.Seed()
	if err != nil {
		return "", "", err
	}
	s.files["keys/"+strings.ToLower(name)+".nk"] = append(seed, '\n')
	return public, encoded, nil
}

func (s *setup) user(name, account string, signer nkeys.KeyPair, permissions agentv1.Permissions, allowResponses bool) error {
	key, err := nkeys.CreateUser()
	if err != nil {
		return err
	}
	public, err := key.PublicKey()
	if err != nil {
		return err
	}
	claims := jwt.NewUserClaims(public)
	claims.Name = name
	claims.IssuerAccount = account
	claims.Pub.Allow.Add(permissions.Publish...)
	claims.Sub.Allow.Add(permissions.Subscribe...)
	if allowResponses {
		claims.Resp = &jwt.ResponsePermission{MaxMsgs: 1, Expires: time.Minute}
	}
	encoded, err := claims.Encode(signer)
	if err != nil {
		return err
	}
	seed, err := key.Seed()
	if err != nil {
		return err
	}
	creds, err := jwt.FormatUserConfig(encoded, seed)
	if err != nil {
		return err
	}
	s.files[name+".creds"] = creds
	return nil
}

// serverConfig is an operator-mode nats-server configuration with the account
// JWTs preloaded into a memory resolver.
func serverConfig(operatorJWT, systemAccount string, accounts map[string]string) string {
	keys := make([]string, 0, len(accounts))
	for key := range accounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var preload strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&preload, "  %s: %q\n", key, accounts[key])
	}
	return fmt.Sprintf(`# Generated by machinarchy deployments/nats/generate. Operator (JWT) mode:
# every client authenticates with a user JWT signed for the accounts below.
port: 4222
server_name: machinarchy-nats

jetstream {
  store_dir: "/data/jetstream"
}

# Production: agents and machinarchy must connect with tls://.
# tls {
#   cert_file: "/etc/nats/tls/server.crt"
#   key_file:  "/etc/nats/tls/server.key"
# }

operator: %q
system_account: %s

resolver: MEMORY
resolver_preload: {
%s}
`, operatorJWT, systemAccount, preload.String())
}

func (s *setup) write() error {
	if err := os.MkdirAll(filepath.Join(s.dir, "keys"), 0o700); err != nil {
		return err
	}
	// #nosec G302 -- a directory needs the execute bit; 0700 is owner-only.
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return err
	}
	for name, content := range s.files {
		if err := os.WriteFile(filepath.Join(s.dir, name), content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

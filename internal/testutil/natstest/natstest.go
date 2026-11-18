// Package natstest runs an embedded NATS server in operator (JWT) mode for
// tests: it trusts a fresh operator, has a system account and one JetStream
// account whose signing key issues user credentials, like a deployment
// created with nsc (deployments/nats).
package natstest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Server is a running operator-mode NATS server.
type Server struct {
	URL string
	// Account is the application account's public key.
	Account string
	// Signer is a signing key of Account (what the controller holds).
	Signer nkeys.KeyPair
	// SignerSeed is Signer's seed, for writing a key file.
	SignerSeed []byte

	dir string
}

// Start runs the server until the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	operatorKey := must(t, nkeys.CreateOperator)
	operatorPublic := mustString(t, operatorKey.PublicKey)

	newAccount := func(name string, jetStream bool, signer nkeys.KeyPair) (string, string) {
		key := must(t, nkeys.CreateAccount)
		public := mustString(t, key.PublicKey)
		claims := jwt.NewAccountClaims(public)
		claims.Name = name
		if signer != nil {
			claims.SigningKeys.Add(mustString(t, signer.PublicKey))
		}
		if jetStream {
			claims.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
		}
		encoded, err := claims.Encode(operatorKey)
		if err != nil {
			t.Fatal(err)
		}
		return public, encoded
	}
	systemPublic, systemJWT := newAccount("SYS", false, nil)
	signer := must(t, nkeys.CreateAccount)
	accountPublic, accountJWT := newAccount("MACHINARCHY", true, signer)

	operatorClaims := jwt.NewOperatorClaims(operatorPublic)
	operatorClaims.SystemAccount = systemPublic
	encodedOperator, err := operatorClaims.Encode(operatorKey)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := jwt.DecodeOperatorClaims(encodedOperator)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &server.MemAccResolver{}
	if err := resolver.Store(accountPublic, accountJWT); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Store(systemPublic, systemJWT); err != nil {
		t.Fatal(err)
	}
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true, TrustedOperators: []*jwt.OperatorClaims{trusted}, AccountResolver: resolver,
		SystemAccount: systemPublic})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("NATS server did not start")
	}
	t.Cleanup(ns.Shutdown)
	seed, err := signer.Seed()
	if err != nil {
		t.Fatal(err)
	}
	return &Server{URL: ns.ClientURL(), Account: accountPublic, Signer: signer, SignerSeed: seed, dir: t.TempDir()}
}

// ControllerCredentials writes a creds file for the controller with the given
// permissions (agentgateway.ControllerPermissions) and permission to answer
// requests, and returns its path.
func (s *Server) ControllerCredentials(t testing.TB, permissions agentv1.Permissions) string {
	t.Helper()
	return s.credentials(t, "controller", &permissions, true)
}

// BootstrapCredentials writes a creds file with agentv1.BootstrapPermissions
// (the shared credential agents use before enrolment).
func (s *Server) BootstrapCredentials(t testing.TB) string {
	t.Helper()
	permissions := agentv1.BootstrapPermissions()
	return s.credentials(t, "bootstrap", &permissions, false)
}

// SignerFile writes the account signing key seed to a 0600 file.
func (s *Server) SignerFile(t testing.TB) string {
	t.Helper()
	path := filepath.Join(s.dir, "account-signing.nk")
	if err := os.WriteFile(path, s.SignerSeed, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (s *Server) credentials(t testing.TB, name string, permissions *agentv1.Permissions, allowResponses bool) string {
	t.Helper()
	key := must(t, nkeys.CreateUser)
	claims := jwt.NewUserClaims(mustString(t, key.PublicKey))
	claims.Name = name
	claims.IssuerAccount = s.Account
	if permissions != nil {
		claims.Pub.Allow.Add(permissions.Publish...)
		claims.Sub.Allow.Add(permissions.Subscribe...)
	}
	if allowResponses {
		claims.Resp = &jwt.ResponsePermission{MaxMsgs: 1, Expires: time.Minute}
	}
	encoded, err := claims.Encode(s.Signer)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := key.Seed()
	if err != nil {
		t.Fatal(err)
	}
	creds, err := jwt.FormatUserConfig(encoded, seed)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, name+".creds")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func must[T any](t testing.TB, fn func() (T, error)) T {
	t.Helper()
	value, err := fn()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustString(t testing.TB, fn func() (string, error)) string {
	t.Helper()
	return must(t, fn)
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// The generated configuration loads in nats-server, and its credentials
// have exactly the intended reach.
func TestGeneratedSetup(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nats-auth")
	if err := run(out, "MACHINARCHY"); err != nil {
		t.Fatal(err)
	}
	if err := run(out, "MACHINARCHY"); err == nil {
		t.Fatal("generate overwrote an existing setup")
	}
	for _, name := range []string{"controller.creds", "bootstrap.creds", "keys/account-signing.nk", "keys/operator.nk", "nats-server.conf"} {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v mode %v, want 0600", name, err, info)
		}
	}

	opts, err := server.ProcessConfigFile(filepath.Join(out, "nats-server.conf"))
	if err != nil {
		t.Fatalf("nats-server rejects the generated config: %v", err)
	}
	opts.Host, opts.Port, opts.StoreDir, opts.NoLog, opts.NoSigs = "127.0.0.1", -1, t.TempDir(), true, true
	ns, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("server did not start")
	}
	t.Cleanup(ns.Shutdown)

	connect := func(creds string, extra ...nats.Option) (*nats.Conn, chan error) {
		errs := make(chan error, 8)
		options := append([]nats.Option{nats.UserCredentials(filepath.Join(out, creds)),
			nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err })}, extra...)
		nc, err := nats.Connect(ns.ClientURL(), options...)
		if err != nil {
			t.Fatalf("connect with %s: %v", creds, err)
		}
		t.Cleanup(nc.Close)
		return nc, errs
	}
	denied := func(what string, nc *nats.Conn, errs chan error, act func() error) {
		t.Helper()
		_ = act()
		_ = nc.Flush()
		select {
		case err := <-errs:
			if !strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
				t.Fatalf("%s: %v", what, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s was allowed", what)
		}
	}

	// The controller can provision JetStream and serve registration.
	controller, controllerErrs := connect("controller.creds", nats.CustomInboxPrefix(agentv1.ControllerInboxPrefix))
	js, err := jetstream.New(controller)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: agentv1.CommandStream,
		Subjects: []string{agentv1.SubjectAllCommands}}); err != nil {
		t.Fatalf("controller cannot create the command stream: %v", err)
	}
	if _, err := controller.Subscribe(agentv1.SubjectRegister, func(m *nats.Msg) { _ = m.Respond([]byte("ok")) }); err != nil {
		t.Fatal(err)
	}
	_ = controller.Flush()

	// The bootstrap credential can only enrol.
	bootstrap, bootstrapErrs := connect("bootstrap.creds")
	reply, err := bootstrap.Request(agentv1.SubjectRegister, []byte("hello"), 3*time.Second)
	if err != nil || string(reply.Data) != "ok" {
		t.Fatalf("bootstrap registration request: %v", err)
	}
	denied("bootstrap reads registrations", bootstrap, bootstrapErrs, func() error {
		_, err := bootstrap.SubscribeSync(agentv1.SubjectRegister)
		return err
	})
	denied("bootstrap uses the JetStream API", bootstrap, bootstrapErrs, func() error {
		return bootstrap.Publish("$JS.API.STREAM.LIST", nil)
	})
	denied("controller reads the default inbox", controller, controllerErrs, func() error {
		_, err := controller.SubscribeSync("_INBOX.>")
		return err
	})
}

package store_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/store"
	_ "modernc.org/sqlite"
)

// The contract suite runs every behavior the application relies on against each
// available engine. SQLite always runs. PostgreSQL runs when
// MACHINARCHY_TEST_POSTGRES_URL points at a server (each test gets its own
// schema, dropped afterwards); set MACHINARCHY_REQUIRE_POSTGRES_TESTS=1 to fail
// instead of skipping when it is missing, as CI does.

const testEncryptionSecret = "integration-test-secret-with-more-than-thirty-two-bytes"

type fixture struct {
	engine string
	cfg    store.Config
	stores *store.Stores
}

func availableEngines(t *testing.T) []string {
	t.Helper()
	engines := []string{store.EngineSQLite}
	switch {
	case os.Getenv("MACHINARCHY_TEST_POSTGRES_URL") != "":
		engines = append(engines, store.EnginePostgres)
	case os.Getenv("MACHINARCHY_REQUIRE_POSTGRES_TESTS") != "":
		t.Fatal("MACHINARCHY_REQUIRE_POSTGRES_TESTS is set but MACHINARCHY_TEST_POSTGRES_URL is empty")
	default:
		t.Log("PostgreSQL contract tests skipped: set MACHINARCHY_TEST_POSTGRES_URL (see CONTRIBUTING.md)")
	}
	return engines
}

func newFixture(t *testing.T, engine string) *fixture {
	t.Helper()
	f := &fixture{engine: engine}
	switch engine {
	case store.EngineSQLite:
		path := filepath.Join(t.TempDir(), "machinarchy.db")
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("restrict test database directory: %v", err)
		}
		f.cfg = store.Config{Engine: engine, SQLitePath: path, EncryptionSecret: testEncryptionSecret}
	case store.EnginePostgres:
		base := os.Getenv("MACHINARCHY_TEST_POSTGRES_URL")
		parsed, err := url.Parse(base)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatalf("MACHINARCHY_TEST_POSTGRES_URL must be a postgres:// URL")
		}
		suffix := make([]byte, 6)
		if _, err := rand.Read(suffix); err != nil {
			t.Fatal(err)
		}
		schema := "contract_" + hex.EncodeToString(suffix)
		admin, err := sql.Open("postgres", base)
		if err != nil {
			t.Fatalf("open PostgreSQL admin connection: %v", err)
		}
		if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
			_ = admin.Close()
			t.Fatalf("create test schema: %v", err)
		}
		t.Cleanup(func() {
			_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
			_ = admin.Close()
		})
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		f.cfg = store.Config{
			Engine: engine, PostgresURL: parsed.String(), EncryptionSecret: testEncryptionSecret,
			MaxOpenConns: 8, ConnectTimeout: 15 * time.Second,
		}
	default:
		t.Fatalf("unknown engine %q", engine)
	}
	f.open(t)
	t.Cleanup(func() { _ = f.stores.Close() })
	return f
}

func (f *fixture) open(t *testing.T) {
	t.Helper()
	stores, err := store.Open(context.Background(), f.cfg)
	if err != nil {
		t.Fatalf("open %s store: %v", f.engine, err)
	}
	f.stores = stores
}

// raw opens a direct connection that bypasses migrations, for repairing or
// inspecting data that store.Open would refuse to start on.
func (f *fixture) raw(t *testing.T) *sql.DB {
	t.Helper()
	driver, dsn := "sqlite", f.cfg.SQLitePath
	if f.engine == store.EnginePostgres {
		driver, dsn = "postgres", f.cfg.PostgresURL
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// pick returns the statement for the fixture's engine (placeholders differ).
func (f *fixture) pick(sqliteSQL, postgresSQL string) string {
	if f.engine == store.EnginePostgres {
		return postgresSQL
	}
	return sqliteSQL
}

func (f *fixture) newWorkspace(t *testing.T, name string) int {
	t.Helper()
	ctx := context.Background()
	var id int
	if f.engine == store.EnginePostgres {
		if err := f.stores.DB.QueryRowContext(ctx, `INSERT INTO workspaces (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
		return id
	}
	result, err := f.stores.DB.ExecContext(ctx, `INSERT INTO workspaces (name, created_at) VALUES (?, ?)`,
		name, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	last, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(last)
}

func (f *fixture) member(t *testing.T, name string, role domain.Role) *domain.User {
	t.Helper()
	ctx := context.Background()
	user, err := f.stores.Users.CreateUser(ctx, name, name+"@example.test", "hash")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if role != domain.RoleReader {
		if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, user.WorkspaceID, user.ID, role); err != nil {
			t.Fatalf("set %s to %s: %v", name, role, err)
		}
	}
	return user
}

func (f *fixture) roleOf(t *testing.T, username string) domain.Role {
	t.Helper()
	user, err := f.stores.Users.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("load %s: %v", username, err)
	}
	return user.Role
}

func contractHost(workspaceID int, name, address string, port int) *domain.Host {
	return &domain.Host{
		WorkspaceID: workspaceID, Name: name, Hostname: name + ".example.test", IPAddress: address,
		Connection: domain.MethodSSH, Labels: map[string]string{"env": "prod"},
		SSH: &domain.SSHAccess{
			Port: port, Username: "deploy", Status: domain.HostStatusPending,
			AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: "pw", ExpectedFingerprint: "SHA256:test"},
		},
	}
}

var contractTests = []struct {
	name string
	run  func(t *testing.T, f *fixture)
}{
	{"signup defaults and uniqueness", testSignup},
	{"member roles and last owner", testMemberRoles},
	{"concurrent owner demotion", testConcurrentDemotion},
	{"host lifecycle and isolation", testHostLifecycle},
	{"host uniqueness", testHostUniqueness},
	{"environments", testEnvironments},
	{"API tokens", testAPITokens},
	{"migrations are idempotent", testMigrationsIdempotent},
	{"host uniqueness pre-flight", testHostUniquenessPreflight},
}

func TestStoreContract(t *testing.T) {
	for _, engine := range availableEngines(t) {
		t.Run(engine, func(t *testing.T) {
			for _, tc := range contractTests {
				t.Run(tc.name, func(t *testing.T) {
					tc.run(t, newFixture(t, engine))
				})
			}
		})
	}
}

func testSignup(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice, err := f.stores.Users.CreateUser(ctx, "alice", "alice@example.test", "hash")
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if alice.ID <= 0 || alice.Role != domain.RoleReader || alice.WorkspaceID != domain.DefaultWorkspaceID {
		t.Fatalf("new user = %+v, want a reader in the default workspace", alice)
	}
	if _, err := f.stores.Users.CreateUser(ctx, "alice", "other@example.test", "hash"); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Errorf("duplicate username error = %v, want ErrUserAlreadyExists", err)
	}
	if _, err := f.stores.Users.CreateUser(ctx, "alice2", "alice@example.test", "hash"); !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Errorf("duplicate email error = %v, want ErrUserAlreadyExists", err)
	}
	// Accounts without an email must not collide with each other.
	for _, name := range []string{"noemail-1", "noemail-2"} {
		if _, err := f.stores.Users.CreateUser(ctx, name, "", "hash"); err != nil {
			t.Errorf("create %s without email: %v", name, err)
		}
	}
	for label, lookup := range map[string]func() (*domain.User, error){
		"username":   func() (*domain.User, error) { return f.stores.Users.GetUserByUsername(ctx, "alice") },
		"email":      func() (*domain.User, error) { return f.stores.Users.GetUserByEmail(ctx, "alice@example.test") },
		"identifier": func() (*domain.User, error) { return f.stores.Users.GetUserByIdentifier(ctx, "alice@example.test") },
	} {
		got, err := lookup()
		if err != nil || got.ID != alice.ID || got.Role != domain.RoleReader || got.WorkspaceID != domain.DefaultWorkspaceID {
			t.Errorf("lookup by %s = %+v, %v; want alice as a default-workspace reader", label, got, err)
		}
	}
	if _, err := f.stores.Users.GetUserByUsername(ctx, "nobody"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown user error = %v, want sql.ErrNoRows", err)
	}
	var workspaces int
	if err := f.stores.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&workspaces); err != nil || workspaces != 1 {
		t.Errorf("workspace rows = %d, %v; signups must not create workspaces", workspaces, err)
	}
}

func testMemberRoles(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice := f.member(t, "alice", domain.RoleOwner)
	bob := f.member(t, "bob", domain.RoleOperator)
	f.member(t, "carol", domain.RoleReader)

	members, err := f.stores.Users.ListWorkspaceMembers(ctx, domain.DefaultWorkspaceID)
	if err != nil || len(members) != 3 {
		t.Fatalf("ListWorkspaceMembers() = %+v, %v; want 3 members", members, err)
	}
	for i, want := range []struct {
		name string
		role domain.Role
	}{{"alice", domain.RoleOwner}, {"bob", domain.RoleOperator}, {"carol", domain.RoleReader}} {
		if members[i].Username != want.name || members[i].Role != want.role || members[i].CreatedAt.IsZero() {
			t.Errorf("member[%d] = %+v, want %s/%s with a creation time", i, members[i], want.name, want.role)
		}
	}
	if other, err := f.stores.Users.ListWorkspaceMembers(ctx, 4242); err != nil || len(other) != 0 {
		t.Errorf("another workspace's members = %v, %v; want none", other, err)
	}

	// Unknown member, and a member of a different workspace.
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, 9999, domain.RoleReader); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("unknown user error = %v, want ErrUserNotFound", err)
	}
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, 4242, alice.ID, domain.RoleReader); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("wrong workspace error = %v, want ErrUserNotFound (no cross-workspace changes)", err)
	}

	// The last owner cannot be demoted; a second owner makes it legal.
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, alice.ID, domain.RoleOperator); !errors.Is(err, domain.ErrLastOwner) {
		t.Fatalf("demote last owner error = %v, want ErrLastOwner", err)
	}
	if got := f.roleOf(t, "alice"); got != domain.RoleOwner {
		t.Fatalf("alice role = %s after a refused demotion, want owner", got)
	}
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, alice.ID, domain.RoleOwner); err != nil {
		t.Errorf("owner -> owner on the last owner: %v", err)
	}
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, bob.ID, domain.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, alice.ID, domain.RoleOperator); err != nil {
		t.Fatalf("demote with two owners: %v", err)
	}
	if owners, err := f.stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID); err != nil || owners != 1 {
		t.Fatalf("owners = %d, %v; want 1", owners, err)
	}
}

func testConcurrentDemotion(t *testing.T, f *fixture) {
	ctx := context.Background()
	alice := f.member(t, "alice", domain.RoleOwner)
	bob := f.member(t, "bob", domain.RoleOwner)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int{alice.ID, bob.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.stores.Users.UpdateWorkspaceMemberRole(ctx, domain.DefaultWorkspaceID, id, domain.RoleReader)
		}()
	}
	wg.Wait()

	succeeded, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrLastOwner):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("succeeded=%d refused=%d; exactly one of two concurrent demotions may win", succeeded, refused)
	}
	if owners, err := f.stores.Users.CountWorkspaceOwners(ctx, domain.DefaultWorkspaceID); err != nil || owners != 1 {
		t.Fatalf("owners = %d, %v; want exactly 1", owners, err)
	}
}

func testHostLifecycle(t *testing.T, f *fixture) {
	ctx := context.Background()
	other := f.newWorkspace(t, "other")

	host := contractHost(1, "web-01", "10.0.0.1", 22)
	if err := f.stores.Hosts.CreateHost(ctx, host); err != nil {
		t.Fatalf("CreateHost() error = %v", err)
	}
	id, err := strconv.Atoi(host.ID)
	if err != nil || id <= 0 {
		t.Fatalf("host ID %q must be a positive integer: %v", host.ID, err)
	}

	got, err := f.stores.Hosts.GetHostByIDInWorkspace(ctx, 1, id)
	if err != nil {
		t.Fatalf("GetHostByIDInWorkspace() error = %v", err)
	}
	if got.Name != "web-01" || got.IPAddress != "10.0.0.1" || got.SSH.Port != 22 || got.SSH.Username != "deploy" ||
		got.Labels["env"] != "prod" || got.SSH.AuthType.ExpectedFingerprint != "SHA256:test" ||
		got.SSH.AuthType.Password != "pw" || got.SSH.Status != domain.HostStatusPending {
		t.Fatalf("stored host does not round-trip, including decrypted credentials: %+v", got)
	}
	if byID, err := f.stores.Hosts.GetHostByID(ctx, id); err != nil || byID.ID != host.ID {
		t.Errorf("GetHostByID() = %+v, %v", byID, err)
	}

	if hosts, err := f.stores.Hosts.ListAllHosts(ctx, 1); err != nil || len(hosts) != 1 {
		t.Errorf("workspace 1 hosts = %d, %v; want 1", len(hosts), err)
	}
	if hosts, err := f.stores.Hosts.ListAllHosts(ctx, other); err != nil || len(hosts) != 0 {
		t.Errorf("other workspace hosts = %d, %v; want 0", len(hosts), err)
	}
	if _, err := f.stores.Hosts.GetHostByIDInWorkspace(ctx, other, id); !errors.Is(err, domain.ErrHostNotFound) {
		t.Errorf("cross-workspace read error = %v, want ErrHostNotFound", err)
	}
	if err := f.stores.Hosts.DeleteHost(ctx, other, host.ID); !errors.Is(err, domain.ErrHostNotFound) {
		t.Errorf("cross-workspace delete error = %v, want ErrHostNotFound", err)
	}

	if err := f.stores.Hosts.UpdateHostStatus(ctx, host.ID, domain.HostStatusActive); err != nil {
		t.Fatalf("UpdateHostStatus() error = %v", err)
	}
	if err := f.stores.Hosts.UpdateHostStatusWithGuard(ctx, id, domain.HostStatusPending, domain.HostStatusDisconnected); !errors.Is(err, domain.ErrConcurrentConflict) {
		t.Errorf("stale guard error = %v, want ErrConcurrentConflict", err)
	}
	if err := f.stores.Hosts.UpdateHostStatusWithGuard(ctx, id, domain.HostStatusActive, domain.HostStatusDisconnected); err != nil {
		t.Errorf("matching guard error = %v", err)
	}
	if err := f.stores.Hosts.UpdateHostStatus(ctx, "999999", domain.HostStatusActive); !errors.Is(err, domain.ErrHostNotFound) {
		t.Errorf("unknown host status update error = %v, want ErrHostNotFound", err)
	}

	got.Name, got.Labels = "web-01-renamed", map[string]string{"env": "staging", "tier": "web"}
	if err := f.stores.Hosts.UpdateHost(ctx, got); err != nil {
		t.Fatalf("UpdateHost() error = %v", err)
	}
	updated, err := f.stores.Hosts.GetHostByIDInWorkspace(ctx, 1, id)
	if err != nil || updated.Name != "web-01-renamed" || updated.Labels["tier"] != "web" || updated.SSH.AuthType.Password != "pw" {
		t.Fatalf("updated host = %+v, %v", updated, err)
	}

	if err := f.stores.Hosts.DeleteHost(ctx, 1, host.ID); err != nil {
		t.Fatalf("DeleteHost() error = %v", err)
	}
	if _, err := f.stores.Hosts.GetHostByIDInWorkspace(ctx, 1, id); !errors.Is(err, domain.ErrHostNotFound) {
		t.Errorf("read after delete error = %v, want ErrHostNotFound", err)
	}
	if err := f.stores.Hosts.DeleteHost(ctx, 1, host.ID); !errors.Is(err, domain.ErrHostNotFound) {
		t.Errorf("second delete error = %v, want ErrHostNotFound", err)
	}
}

func testHostUniqueness(t *testing.T, f *fixture) {
	ctx := context.Background()
	for _, h := range []*domain.Host{
		contractHost(1, "Web-01", "10.0.0.1", 22),
		contractHost(1, "dns-1", "Db.Example.Test", 22),
	} {
		if err := f.stores.Hosts.CreateHost(ctx, h); err != nil {
			t.Fatalf("create %s: %v", h.Name, err)
		}
	}
	conflicts := map[string]*domain.Host{
		"same name, different case":     contractHost(1, "WEB-01", "10.0.0.2", 22),
		"same address and port":         contractHost(1, "other", "10.0.0.1", 22),
		"same hostname, different case": contractHost(1, "dns-2", "db.example.test", 22),
	}
	for name, host := range conflicts {
		if err := f.stores.Hosts.CreateHost(ctx, host); !errors.Is(err, domain.ErrHostAlreadyExists) {
			t.Errorf("%s: error = %v, want ErrHostAlreadyExists", name, err)
		}
	}
	if err := f.stores.Hosts.CreateHost(ctx, contractHost(1, "web-01-alt-port", "10.0.0.1", 2222)); err != nil {
		t.Errorf("the same address on a different port is a different target: %v", err)
	}
	other := f.newWorkspace(t, "workspace-b")
	if err := f.stores.Hosts.CreateHost(ctx, contractHost(other, "web-01", "10.0.0.1", 22)); err != nil {
		t.Errorf("the same name and address in another workspace must be allowed: %v", err)
	}
	if hosts, err := f.stores.Hosts.ListAllHosts(ctx, 1); err != nil || len(hosts) != 3 {
		t.Fatalf("workspace 1 hosts = %d, %v; rejected inserts must leave no rows", len(hosts), err)
	}

	// Updates cannot collide either, and a refused update changes nothing.
	second := contractHost(1, "db-01", "10.0.0.9", 22)
	if err := f.stores.Hosts.CreateHost(ctx, second); err != nil {
		t.Fatal(err)
	}
	second.Name = "WEB-01"
	if err := f.stores.Hosts.UpdateHost(ctx, second); !errors.Is(err, domain.ErrHostAlreadyExists) {
		t.Fatalf("rename onto an existing name error = %v, want ErrHostAlreadyExists", err)
	}
	second.Name, second.IPAddress = "db-01", "10.0.0.1"
	if err := f.stores.Hosts.UpdateHost(ctx, second); !errors.Is(err, domain.ErrHostAlreadyExists) {
		t.Fatalf("move onto an existing address:port error = %v, want ErrHostAlreadyExists", err)
	}
	id, _ := strconv.Atoi(second.ID)
	stored, err := f.stores.Hosts.GetHostByIDInWorkspace(ctx, 1, id)
	if err != nil || stored.Name != "db-01" || stored.IPAddress != "10.0.0.9" {
		t.Fatalf("stored host = %+v, %v; a refused update must change nothing", stored, err)
	}
	second.IPAddress = "10.0.0.9"
	second.SSH.Username = "ops"
	if err := f.stores.Hosts.UpdateHost(ctx, second); err != nil {
		t.Fatalf("saving a host without changing its identity must not conflict with itself: %v", err)
	}
}

func testEnvironments(t *testing.T, f *fixture) {
	ctx := context.Background()
	other := f.newWorkspace(t, "other")

	production := &domain.Environment{WorkspaceID: 1, Name: "production", Description: "prod"}
	if err := f.stores.Environments.CreateEnvironment(ctx, production); err != nil || production.ID <= 0 {
		t.Fatalf("CreateEnvironment() = %+v, %v", production, err)
	}
	if err := f.stores.Environments.CreateEnvironment(ctx, &domain.Environment{WorkspaceID: 1, Name: "production"}); !errors.Is(err, domain.ErrEnvironmentAlreadyExists) {
		t.Errorf("duplicate name error = %v, want ErrEnvironmentAlreadyExists", err)
	}
	foreign := &domain.Environment{WorkspaceID: other, Name: "production", Description: "same name elsewhere"}
	if err := f.stores.Environments.CreateEnvironment(ctx, foreign); err != nil {
		t.Fatalf("the same name in another workspace must be allowed: %v", err)
	}

	if list, err := f.stores.Environments.ListEnvironments(ctx, 1); err != nil || len(list) != 1 || list[0].Description != "prod" {
		t.Errorf("workspace 1 environments = %+v, %v", list, err)
	}
	if _, err := f.stores.Environments.GetEnvironmentByID(ctx, other, production.ID); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Errorf("cross-workspace read error = %v, want ErrEnvironmentNotFound", err)
	}
	if err := f.stores.Environments.DeleteEnvironment(ctx, other, production.ID); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Errorf("cross-workspace delete error = %v, want ErrEnvironmentNotFound", err)
	}

	// Hosts may only reference environments of their own workspace.
	crossHost := contractHost(1, "cross", "10.0.1.1", 22)
	crossHost.EnvironmentID = foreign.ID
	if err := f.stores.Hosts.CreateHost(ctx, crossHost); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Errorf("host referencing another workspace's environment error = %v, want ErrEnvironmentNotFound", err)
	}
	ownHost := contractHost(1, "own", "10.0.1.2", 22)
	ownHost.EnvironmentID = production.ID
	if err := f.stores.Hosts.CreateHost(ctx, ownHost); err != nil {
		t.Errorf("host referencing its own environment: %v", err)
	}

	production.Description = "production systems"
	if err := f.stores.Environments.UpdateEnvironment(ctx, production); err != nil {
		t.Fatalf("UpdateEnvironment() error = %v", err)
	}
	if got, err := f.stores.Environments.GetEnvironmentByID(ctx, 1, production.ID); err != nil || got.Description != "production systems" {
		t.Errorf("updated environment = %+v, %v", got, err)
	}
	staging := &domain.Environment{WorkspaceID: 1, Name: "staging"}
	if err := f.stores.Environments.CreateEnvironment(ctx, staging); err != nil {
		t.Fatal(err)
	}
	staging.Name = "production"
	if err := f.stores.Environments.UpdateEnvironment(ctx, staging); !errors.Is(err, domain.ErrEnvironmentAlreadyExists) {
		t.Errorf("rename onto an existing name error = %v, want ErrEnvironmentAlreadyExists", err)
	}
	if err := f.stores.Environments.DeleteEnvironment(ctx, 1, staging.ID); err != nil {
		t.Errorf("DeleteEnvironment() error = %v", err)
	}
	if _, err := f.stores.Environments.GetEnvironmentByID(ctx, 1, staging.ID); !errors.Is(err, domain.ErrEnvironmentNotFound) {
		t.Errorf("read after delete error = %v, want ErrEnvironmentNotFound", err)
	}
}

func testAPITokens(t *testing.T, f *fixture) {
	ctx := context.Background()
	f.member(t, "alice", domain.RoleOwner)
	bob := f.member(t, "bob", domain.RoleOperator)

	newToken := func(name, hashChar string, expires time.Time) *domain.APIToken {
		return &domain.APIToken{
			UserID: bob.ID, WorkspaceID: 1, Name: name, Role: domain.RoleOperator,
			TokenHash: strings.Repeat(hashChar, 64), CreatedAt: time.Now().UTC(), ExpiresAt: &expires,
		}
	}
	active := newToken("ci", "a", time.Now().Add(time.Hour))
	if err := f.stores.Access.CreateAPIToken(ctx, active); err != nil || active.ID <= 0 {
		t.Fatalf("CreateAPIToken() = %+v, %v", active, err)
	}
	if err := f.stores.Access.CreateAPIToken(ctx, newToken("ci", "b", time.Now().Add(time.Hour))); !errors.Is(err, domain.ErrAPITokenAlreadyExists) {
		t.Errorf("duplicate token name error = %v, want ErrAPITokenAlreadyExists", err)
	}

	principal, err := f.stores.Access.ResolveAPIToken(ctx, active.TokenHash)
	if err != nil {
		t.Fatalf("ResolveAPIToken() error = %v", err)
	}
	if principal.UserID != bob.ID || principal.WorkspaceID != 1 || principal.APITokenID != active.ID || principal.Role != domain.RoleOperator {
		t.Fatalf("principal = %+v, want bob's operator token", principal)
	}

	// A token never exceeds the member's current role.
	if err := f.stores.Users.UpdateWorkspaceMemberRole(ctx, 1, bob.ID, domain.RoleReader); err != nil {
		t.Fatal(err)
	}
	if principal, err := f.stores.Access.ResolveAPIToken(ctx, active.TokenHash); err != nil || principal.Role != domain.RoleReader {
		t.Errorf("after a membership downgrade principal = %+v, %v; want reader", principal, err)
	}

	expired := newToken("old", "c", time.Now().Add(-time.Hour))
	if err := f.stores.Access.CreateAPIToken(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stores.Access.ResolveAPIToken(ctx, expired.TokenHash); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Errorf("expired token error = %v, want ErrAPITokenNotFound", err)
	}
	if _, err := f.stores.Access.ResolveAPIToken(ctx, "short"); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Errorf("malformed hash error = %v, want ErrAPITokenNotFound", err)
	}

	if tokens, err := f.stores.Access.ListAPITokens(ctx, bob.ID, 1); err != nil || len(tokens) != 2 {
		t.Errorf("bob's tokens = %d, %v; want 2", len(tokens), err)
	}
	if err := f.stores.Access.RevokeAPIToken(ctx, active.ID, bob.ID+1000, 1); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Errorf("revoking another user's token error = %v, want ErrAPITokenNotFound", err)
	}
	if err := f.stores.Access.RevokeAPIToken(ctx, active.ID, bob.ID, 1); err != nil {
		t.Fatalf("RevokeAPIToken() error = %v", err)
	}
	if _, err := f.stores.Access.ResolveAPIToken(ctx, active.TokenHash); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Errorf("revoked token error = %v, want ErrAPITokenNotFound", err)
	}
}

func countMigrations(t *testing.T, f *fixture) int {
	t.Helper()
	var count int
	if err := f.stores.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count applied migrations: %v", err)
	}
	return count
}

func testMigrationsIdempotent(t *testing.T, f *fixture) {
	ctx := context.Background()
	f.member(t, "alice", domain.RoleOwner)
	before := countMigrations(t, f)
	if before < 4 {
		t.Fatalf("applied migrations = %d, want at least 4", before)
	}
	if err := f.stores.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	if after := countMigrations(t, f); after != before {
		t.Errorf("reopening changed the applied migrations from %d to %d", before, after)
	}
	if user, err := f.stores.Users.GetUserByUsername(ctx, "alice"); err != nil || user.Role != domain.RoleOwner {
		t.Errorf("data after reopen = %+v, %v", user, err)
	}
	if err := f.stores.Ping(ctx); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

func testHostUniquenessPreflight(t *testing.T, f *fixture) {
	ctx := context.Background()

	// Make the database look like it was last migrated before version 4 and hold
	// hosts that violate the new indexes.
	for _, statement := range []string{
		`DROP INDEX idx_hosts_workspace_name`,
		`DROP INDEX idx_hosts_workspace_address`,
		`DELETE FROM schema_migrations WHERE version = 4`,
	} {
		if _, err := f.stores.DB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	insert := f.pick(
		`INSERT INTO hosts (id, workspace_id, environment_id, name, hostname, ip_address,
			connection, status, port, username, password, private_key, passphrase, expected_fingerprint,
			created_at, updated_at) VALUES (?, 1, 0, ?, 'h', ?, 'SSH', 'PENDING', 22, 'u', '', '', '', 'fp', 'now', 'now')`,
		`INSERT INTO hosts (id, workspace_id, environment_id, name, hostname, ip_address,
			connection, status, port, username, password, private_key, passphrase, expected_fingerprint,
			created_at, updated_at) VALUES ($1, 1, 0, $2, 'h', $3, 'SSH', 'PENDING', 22, 'u', '', '', '', 'fp', NOW(), NOW())`,
	)
	for _, row := range [][]any{
		{"901", "web-01", "10.0.0.1"},
		{"902", "WEB-01", "10.0.0.2"}, // name duplicate
		{"903", "app-01", "10.0.0.5"},
		{"904", "app-02", "10.0.0.5"}, // address:port duplicate
	} {
		if _, err := f.stores.DB.ExecContext(ctx, insert, row...); err != nil {
			t.Fatalf("seed host %v: %v", row[0], err)
		}
	}
	if err := f.stores.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := store.Open(ctx, f.cfg)
	if err == nil {
		t.Fatal("Open() succeeded although existing hosts violate the uniqueness constraints")
	}
	for _, want := range []string{"web-01", "workspace 1", "901", "902", "10.0.0.5:22", "903", "904", "Nothing has been changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}

	// Resolve the conflicts by hand, as the message instructs.
	raw := f.raw(t)
	if _, err := raw.ExecContext(ctx, `DELETE FROM hosts WHERE id IN ('902', '904')`); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	var indexes int
	countIndexes := f.pick(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('idx_hosts_workspace_name', 'idx_hosts_workspace_address')`,
		`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname IN ('idx_hosts_workspace_name', 'idx_hosts_workspace_address')`,
	)
	if err := f.stores.DB.QueryRowContext(ctx, countIndexes).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("uniqueness indexes present = %d, %v; want 2", indexes, err)
	}
}

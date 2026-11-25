package deprecate_test

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli/deprecate"
)

func tree() *cobra.Command {
	noop := func(*cobra.Command, []string) {}
	root := &cobra.Command{Use: "machinarchy"}
	get := &cobra.Command{Use: "get", Run: noop}
	get.AddCommand(&cobra.Command{Use: "hosts", Run: noop}, &cobra.Command{Use: "profiles", Run: noop})
	root.AddCommand(get,
		&cobra.Command{Use: "hosts", Run: noop},
		&cobra.Command{Use: "access", Run: noop},
		&cobra.Command{Use: "run", Run: noop},
	)

	return root
}

func TestApplyHidesAndRecordsTheReplacement(t *testing.T) {
	t.Parallel()

	root := tree()
	err := deprecate.Apply(root, []deprecate.Alias{
		{Old: "get hosts", New: "machinarchy hosts", RemovedIn: "0.2.0"},
		{Old: "get profiles", New: "machinarchy access"},
	})
	if err != nil {
		t.Fatal(err)
	}

	hosts, _, _ := root.Find([]string{"get", "hosts"})
	if !hosts.Hidden {
		t.Fatal("a deprecated command must be hidden from help and completion")
	}
	if replacement, _, _ := root.Find([]string{"hosts"}); replacement.Hidden {
		t.Fatal("the replacement must stay visible")
	}

	notice, ok := deprecate.For(hosts)
	if !ok {
		t.Fatal("no notice for a deprecated command")
	}
	want := `Deprecated: "machinarchy get hosts" is now "machinarchy hosts". It will be removed in 0.2.0.`
	if got := notice.Message().Primary; got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}

	profiles, _, _ := root.Find([]string{"get", "profiles"})
	notice, _ = deprecate.For(profiles)
	if !strings.HasSuffix(notice.Message().Primary, "It will be removed in the next release.") {
		t.Fatalf("a missing release must read %q: %q", deprecate.DefaultRemovedIn, notice.Message().Primary)
	}
}

func TestForFindsTheNearestDeprecatedAncestor(t *testing.T) {
	t.Parallel()

	root := tree()
	get, _, _ := root.Find([]string{"get"})
	deprecate.Mark(get, deprecate.Alias{New: "machinarchy hosts"})

	child, _, _ := root.Find([]string{"get", "hosts"})
	notice, ok := deprecate.For(child)
	if !ok || notice.Old != "machinarchy get" || notice.New != "machinarchy hosts" {
		t.Fatalf("notice = %+v, %v", notice, ok)
	}

	// The command's own mark wins over its parent's.
	deprecate.Mark(child, deprecate.Alias{New: "machinarchy access"})
	notice, _ = deprecate.For(child)
	if notice.New != "machinarchy access" || notice.Old != "machinarchy get hosts" {
		t.Fatalf("own mark must win: %+v", notice)
	}

	if _, ok := deprecate.For(root); ok {
		t.Fatal("the root is not deprecated")
	}
	if _, ok := deprecate.For(nil); ok {
		t.Fatal("nil is not deprecated")
	}
}

func TestApplyReportsEveryMissingCommand(t *testing.T) {
	t.Parallel()

	err := deprecate.Apply(tree(), []deprecate.Alias{
		{Old: "get nothing", New: "machinarchy hosts"},
		{Old: "gone", New: "machinarchy hosts"},
		{Old: "get hosts", New: "machinarchy hosts"},
	})
	if err == nil || !strings.Contains(err.Error(), `"get nothing"`) || !strings.Contains(err.Error(), `"gone"`) {
		t.Fatalf("err = %v", err)
	}
}

// U-09: every deprecation names a replacement that exists.
func TestValidate(t *testing.T) {
	t.Parallel()

	root := tree()
	good := []deprecate.Alias{
		{Old: "get hosts", New: "machinarchy hosts"},
		{Old: "get profiles", New: "machinarchy access"},
		{Old: "get", New: "machinarchy hosts --on tier=web"}, // flags in a replacement are ignored
	}
	if problems := deprecate.Validate(root, good); len(problems) != 0 {
		t.Fatalf("a valid table has problems: %v", problems)
	}

	bad := []deprecate.Alias{
		{Old: "get hosts", New: "machinarchy hostz"},     // replacement does not exist
		{Old: "get nothing", New: "machinarchy hosts"},   // old command does not exist
		{Old: "get profiles", New: "hosts"},              // does not start with the root name
		{Old: "get profiles", New: "machinarchy access"}, // duplicate old
	}
	problems := strings.Join(deprecate.Validate(root, bad), "\n")
	for _, want := range []string{
		`replacement "machinarchy hostz" for "get hosts" is not a command`,
		`old command "get nothing" does not exist`,
		`replacement "hosts" for "get profiles" must start with "machinarchy"`,
		`"get profiles" appears twice`,
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("missing problem %q in:\n%s", want, problems)
		}
	}

	// A replacement may not itself be deprecated: chains hide the real command.
	chained := tree()
	if err := deprecate.Apply(chained, []deprecate.Alias{{Old: "hosts", New: "machinarchy access"}}); err != nil {
		t.Fatal(err)
	}
	problems = strings.Join(deprecate.Validate(chained, []deprecate.Alias{{Old: "get hosts", New: "machinarchy hosts"}}), "\n")
	if !strings.Contains(problems, "is itself deprecated") {
		t.Fatalf("a deprecated replacement must be reported:\n%s", problems)
	}
}

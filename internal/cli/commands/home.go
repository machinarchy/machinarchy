package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// The home screen: bare `machinarchy` (docs/cli/spec/03-ux.md section 1). It
// never prompts. It makes at most three parallel requests, each limited to
// homeBudget, and a failed request degrades one line, not the whole screen.

var homeBudget = 3 * time.Second

const (
	homeHostLimit   = 500
	homeRunLimit    = 2
	homeDetailLimit = 5
)

// Home states, also the "state" field of `-o json`.
const (
	homeNotSignedIn = "not_signed_in"
	homeExpired     = "expired"
	homeUnreachable = "unreachable"
	homeNoHosts     = "no_hosts"
	homeAttention   = "attention"
	homeOK          = "ok"
)

// homeHosts is the host summary of the home document.
type homeHosts struct {
	Counts map[string]int `json:"counts" yaml:"counts"`
	Total  int            `json:"total" yaml:"total"`
	// Truncated is true when more hosts exist than were counted.
	Truncated bool          `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	Problems  []homeProblem `json:"problems,omitempty" yaml:"problems,omitempty"`
}

// homeProblem is a host that needs a person.
type homeProblem struct {
	Name   string `json:"name" yaml:"name"`
	Status string `json:"status" yaml:"status"`
}

// homeRun is one recent run of the home document.
type homeRun struct {
	ID        string         `json:"id" yaml:"id"`
	State     string         `json:"state" yaml:"state"`
	Targets   map[string]int `json:"targets" yaml:"targets"`
	CreatedAt time.Time      `json:"createdAt" yaml:"createdAt"`
}

// homeDocument is the stable machine-readable form of the home screen.
type homeDocument struct {
	State       string     `json:"state" yaml:"state"`
	Server      string     `json:"server,omitempty" yaml:"server,omitempty"`
	URL         string     `json:"url,omitempty" yaml:"url,omitempty"`
	User        string     `json:"user,omitempty" yaml:"user,omitempty"`
	Role        string     `json:"role,omitempty" yaml:"role,omitempty"`
	Environment string     `json:"environment,omitempty" yaml:"environment,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
	Hosts       *homeHosts `json:"hosts,omitempty" yaml:"hosts,omitempty"`
	HostsError  string     `json:"hostsError,omitempty" yaml:"hostsError,omitempty"`
	Runs        []homeRun  `json:"runs,omitempty" yaml:"runs,omitempty"`
	RunsError   string     `json:"runsError,omitempty" yaml:"runsError,omitempty"`
	Error       string     `json:"error,omitempty" yaml:"error,omitempty"`
	Next        string     `json:"next,omitempty" yaml:"next,omitempty"`

	// hostList keeps the hosts for the text screen and the suggestion.
	hostList []api.Host
	runs     []api.OperationSummary
	// exit is the exit code of the screen.
	exit int
}

// Home prints the home screen. It is the action of the bare root command.
func Home(f *cli.Factory, command *cobra.Command) error {
	out := command.OutOrStdout()
	printer, err := f.Printer(out, output.FormatTable)
	if err != nil {
		return err
	}

	doc, err := buildHome(command.Context(), f)
	if err != nil {
		return err
	}

	if printer.Format() != output.FormatTable {
		if err := printer.Document(doc); err != nil {
			return err
		}
	} else if err := writeHome(out, f, doc); err != nil {
		return err
	}
	if doc.exit != 0 {
		return clierr.Reported(doc.exit)
	}

	return nil
}

func buildHome(ctx context.Context, f *cli.Factory) (*homeDocument, error) {
	target, err := f.ResolveTarget()
	if err != nil {
		return homeWithoutCredential(f, err)
	}

	doc := &homeDocument{Server: target.ServerName, URL: target.Server, Environment: target.Environment}
	client, err := f.ClientFor(target)
	if err != nil {
		return nil, err
	}
	auth, ok := client.(api.AuthClient)
	if !ok {
		return nil, errors.New("this client cannot identify the caller")
	}

	var (
		wg       sync.WaitGroup
		user     *api.User
		userErr  error
		hosts    []api.Host
		more     bool
		hostsErr error
		runs     []api.OperationSummary
		runsErr  error
	)
	within := func(call func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			budget, cancel := context.WithTimeout(ctx, homeBudget)
			defer cancel()
			call(budget)
		}()
	}
	within(func(c context.Context) { user, userErr = auth.Me(c) })
	within(func(c context.Context) {
		var id int
		if id, hostsErr = resolveEnvironment(c, client, target.Environment); hostsErr != nil {
			return
		}
		var page api.Page[api.Host]
		if page, hostsErr = client.ListHosts(c, api.HostQuery{EnvironmentID: id, Limit: homeHostLimit}); hostsErr == nil {
			hosts, more = page.Items, page.NextCursor != ""
		}
	})
	within(func(c context.Context) {
		var page api.Page[api.OperationSummary]
		if page, runsErr = client.ListOperations(c, api.OperationQuery{Limit: homeRunLimit}); runsErr == nil {
			runs = page.Items
		}
	})
	wg.Wait()

	if userErr != nil {
		var apiErr *api.Error
		if errors.As(userErr, &apiErr) && apiErr.ExitCode() == clierr.ExitAuth {
			doc.State, doc.exit = homeExpired, clierr.ExitAuth
			doc.Error = "The server did not accept the saved credential."
			doc.Next = "machinarchy login"

			return doc, nil
		}
		doc.State, doc.exit = homeUnreachable, clierr.ExitUnavailable
		doc.Error = sanitize.Line(reasonOf(userErr))
		doc.Next = "machinarchy switch"

		return doc, nil
	}

	doc.User = user.Label()
	switch {
	case target.Kind == cred.KindSession && !target.FromEnv:
		doc.Role = "owner"
	default:
		doc.Role = target.Role
	}
	doc.ExpiresAt = savedExpiry(f, target.ServerName, target.Kind, target.FromEnv)

	if hostsErr != nil {
		doc.HostsError = sanitize.Line(reasonOf(hostsErr))
	} else {
		doc.hostList = hosts
		doc.Hosts = summariseHosts(hosts, more)
	}
	if runsErr != nil {
		doc.RunsError = sanitize.Line(reasonOf(runsErr))
	} else {
		doc.runs = runs
		for _, run := range runs {
			doc.Runs = append(doc.Runs, homeRun{ID: run.ID, State: run.State, Targets: run.Targets, CreatedAt: run.CreatedAt})
		}
	}
	doc.Next = homeSuggestion(doc)
	doc.State = homeOK
	switch {
	case doc.Hosts != nil && doc.Hosts.Total == 0:
		doc.State = homeNoHosts
	case doc.Hosts != nil && len(doc.Hosts.Problems) > 0:
		doc.State = homeAttention
	}

	return doc, nil
}

// reasonOf is a short cause for a failed request, without the API's prefix.
func reasonOf(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("the server answered HTTP %d: %s", apiErr.Status, apiErr.Message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer within " + homeBudget.String()
	}

	return strings.TrimSpace(err.Error())
}

// homeWithoutCredential builds the screen for "not signed in" and "expired".
// Any other failure to pick a server or credential is a real error.
func homeWithoutCredential(f *cli.Factory, err error) (*homeDocument, error) {
	if code, ok := clierr.Classify(err); !ok || code != clierr.ExitAuth {
		return nil, err
	}

	doc := &homeDocument{State: homeNotSignedIn, exit: clierr.ExitAuth, Next: "machinarchy login"}
	name := f.Config().ResolveServerName()
	if name == "" || f.Credentials() == nil {
		return doc, nil
	}
	if server, found := f.Config().Server(name); found {
		doc.Server, doc.URL = name, server.URL
	}
	entry, getErr := f.Credentials().Get(name)
	if getErr != nil {
		return doc, nil
	}
	now := f.Now()
	for _, saved := range []*cred.Credential{entry.Session, entry.Token} {
		if saved != nil && saved.Expired(now) {
			doc.State = homeExpired
			doc.ExpiresAt = saved.ExpiresAt

			break
		}
	}

	return doc, nil
}

// savedExpiry is when the saved credential stops working, as far as the CLI
// knows. The home screen does not ask the server for it (that would be a
// fourth request).
func savedExpiry(f *cli.Factory, server string, kind cred.Kind, fromEnv bool) *time.Time {
	if fromEnv || server == "" || f.Credentials() == nil {
		return nil
	}
	entry, err := f.Credentials().Get(server)
	if err != nil {
		return nil
	}
	saved := entry.Token
	if kind == cred.KindSession {
		saved = entry.Session
	}
	if saved == nil {
		return nil
	}

	return saved.ExpiresAt
}

// summariseHosts counts hosts by readiness and lists the ones that need a
// person, most urgent first.
func summariseHosts(hosts []api.Host, truncated bool) *homeHosts {
	summary := &homeHosts{Counts: map[string]int{}, Total: len(hosts), Truncated: truncated}
	for _, host := range hosts {
		status, _ := hostReadiness(host)
		summary.Counts[status]++
	}
	for _, host := range sortedByUrgency(hosts) {
		if status, _ := hostReadiness(host); status != statusReady && status != statusChecking {
			summary.Problems = append(summary.Problems, homeProblem{Name: host.Metadata.Name, Status: status})
		}
	}

	return summary
}

// urgency orders readiness values by how soon a person must act.
func urgency(status string) int {
	switch status {
	case statusKeyChange:
		return 0
	case statusKey:
		return 1
	case statusNoConnect:
		return 2
	case statusAttention:
		return 3
	}

	return 4
}

func sortedByUrgency(hosts []api.Host) []api.Host {
	sorted := append([]api.Host(nil), hosts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, _ := hostReadiness(sorted[i])
		b, _ := hostReadiness(sorted[j])
		if urgency(a) != urgency(b) {
			return urgency(a) < urgency(b)
		}

		return sorted[i].Metadata.Name < sorted[j].Metadata.Name
	})

	return sorted
}

// homeSuggestion is the one next step (03-ux.md section 1): the most urgent
// problem first, then adding a host, then an example run.
func homeSuggestion(doc *homeDocument) string {
	if doc.HostsError != "" {
		return "machinarchy hosts"
	}
	for _, host := range sortedByUrgency(doc.hostList) {
		status, _ := hostReadiness(host)
		name := sanitize.Line(host.Metadata.Name)
		switch status {
		case statusKeyChange, statusNoConnect, statusAttention:
			return "machinarchy hosts show " + name
		case statusKey:
			return "machinarchy hosts trust " + name
		}
	}
	if len(doc.hostList) == 0 {
		return "machinarchy hosts add"
	}
	if tag := firstTag(doc.hostList); tag != "" {
		return "machinarchy run --on " + tag + " -- uptime"
	}

	return "machinarchy run --on all -- uptime"
}

// firstTag returns the alphabetically first tag (key=value) that is safe to
// show in a command, or "".
func firstTag(hosts []api.Host) string {
	best := ""
	for _, host := range hosts {
		for key, value := range host.Metadata.Labels {
			if reservedLabelKeys[key] || value == "" || !labelKeyPattern.MatchString(key) || !labelValuePattern.MatchString(value) {
				continue
			}
			if tag := key + "=" + value; best == "" || tag < best {
				best = tag
			}
		}
	}

	return best
}

// writeHome prints the text screen to out.
func writeHome(out io.Writer, f *cli.Factory, doc *homeDocument) error {
	style := f.StyleFor(out)
	var b strings.Builder
	dot := " · "
	if !style.Unicode || style.Plain {
		dot = " - "
	}
	vars := msg.Vars{"server": sanitize.Line(doc.Server), "time": ""}
	if doc.ExpiresAt != nil {
		vars["time"] = doc.ExpiresAt.Local().Format("15:04")
	}

	switch doc.State {
	case homeNotSignedIn:
		b.WriteString(f.Logotype(out))
		b.WriteString(ui.Render(msg.Get("home.not_signed_in"), style))
	case homeExpired:
		m := msg.Get("home.expired").With(vars)
		if doc.ExpiresAt == nil {
			m.Primary = "The server did not accept your saved sign-in on " + vars["server"] + "."
		}
		b.WriteString(ui.Render(m, style))
	case homeUnreachable:
		server := sanitize.Line(doc.Server)
		if server == "" {
			server = sanitize.Line(doc.URL)
		} else {
			server += " (" + sanitize.Line(doc.URL) + ")"
		}
		b.WriteString(style.Paint(ui.RoleBold, "machinarchy") + dot + server + "\n")
		b.WriteString(ui.Render(msg.Get("home.unreachable").With(msg.Vars{"why": doc.Error}), style))
	default:
		writeSignedInHome(&b, f, out, style, dot, doc)
	}

	_, err := io.WriteString(out, b.String())

	return err
}

func writeSignedInHome(b *strings.Builder, f *cli.Factory, out io.Writer, style ui.Style, dot string, doc *homeDocument) {
	env := doc.Environment
	if env == "" {
		env = "none"
	}
	left := style.Paint(ui.RoleBold, "machinarchy") + dot + sanitize.Line(displayServer(doc)) + dot + sanitize.Line(env)
	plainLeft := "machinarchy" + dot + sanitize.Line(displayServer(doc)) + dot + sanitize.Line(env)
	right := sanitize.Line(doc.User)
	if doc.Role != "" {
		right += " (" + sanitize.Line(doc.Role) + ")"
	}
	pad := max(f.Width(out)-len([]rune(plainLeft))-len([]rune(right)), 2)
	b.WriteString(left + strings.Repeat(" ", pad) + right + "\n")
	if doc.ExpiresAt != nil {
		if remaining := doc.ExpiresAt.Sub(f.Now()); remaining > 0 {
			b.WriteString("Session ends in " + humanShort(remaining) + "\n")
		}
	}
	b.WriteString("\n")

	const label = 11 // "Last runs  "
	indent := strings.Repeat(" ", label)
	switch {
	case doc.HostsError != "":
		fmt.Fprintf(b, "%-*s%s\n", label, "Hosts", style.Marker(ui.SymWarn)+" not available: "+doc.HostsError)
	case doc.Hosts.Total == 0:
		b.WriteString(msg.Get("home.no_hosts").Primary + "\n")
	default:
		fmt.Fprintf(b, "%-*s%s\n", label, "Hosts", hostsSummaryLine(doc.Hosts, dot))
		for i, problem := range doc.Hosts.Problems {
			if i == homeDetailLimit {
				fmt.Fprintf(b, "%s+%d more: machinarchy hosts\n", indent, len(doc.Hosts.Problems)-i)

				break
			}
			fmt.Fprintf(b, "%s%s  %s\n", indent, sanitize.Line(problem.Name), problem.Status)
		}
	}

	if doc.RunsError != "" {
		fmt.Fprintf(b, "%-*s%s\n", label, "Last runs", style.Marker(ui.SymWarn)+" not available: "+doc.RunsError)
	} else if len(doc.runs) == 0 {
		fmt.Fprintf(b, "%-*snone yet\n", label, "Last runs")
	} else {
		idWidth, resultWidth := 0, 0
		results := make([]string, len(doc.runs))
		for i, run := range doc.runs {
			results[i] = runResultText(run, style)
			idWidth = max(idWidth, len([]rune(sanitize.Line(run.ID))))
			resultWidth = max(resultWidth, visibleWidth(results[i]))
		}
		for i, run := range doc.runs {
			prefix := indent
			if i == 0 {
				prefix = fmt.Sprintf("%-*s", label, "Last runs")
			}
			id := sanitize.Line(run.ID)
			b.WriteString(prefix + id + strings.Repeat(" ", idWidth-len([]rune(id))+2) + results[i] +
				strings.Repeat(" ", resultWidth-visibleWidth(results[i])+2) + ago(f.Now(), run.CreatedAt) + "\n")
		}
	}

	if doc.Next != "" {
		b.WriteString("\n")
		writeNextLine(b, style, doc.Next)
	}
}

// displayServer is the server's saved name, or its address when it has none.
func displayServer(doc *homeDocument) string {
	if doc.Server != "" {
		return doc.Server
	}

	return doc.URL
}

func writeNextLine(b *strings.Builder, style ui.Style, next string) {
	b.WriteString("  " + style.Paint(ui.RoleDim, "Next:") + "  " + style.Paint(ui.RoleAccent, sanitize.Line(next)) + "\n")
}

// hostsSummaryLine is "12 ready · 1 waiting for key approval · 1 cannot connect".
func hostsSummaryLine(h *homeHosts, dot string) string {
	order := []string{statusReady, statusKeyChange, statusKey, statusNoConnect, statusAttention, statusChecking}
	var parts []string
	for _, status := range order {
		if n := h.Counts[status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, status))
		}
	}
	line := strings.Join(parts, dot)
	if h.Truncated {
		line += fmt.Sprintf(" (first %d counted)", h.Total)
	}

	return line
}

// humanShort writes a remaining time as "21 h", "35 min" or "2 days".
func humanShort(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%d h", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}

	return "under a minute"
}

// visibleWidth counts the characters of s that are not ANSI colour codes.
func visibleWidth(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			n++
		}
	}

	return n
}

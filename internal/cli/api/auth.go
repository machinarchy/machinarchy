package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

// Endpoint is a server the CLI talks to before it has a credential: to check
// that it is a Machinarchy server and to sign in.
type Endpoint struct {
	// URL is the API base URL.
	URL string
	// Timeout bounds each request; zero selects the default.
	Timeout time.Duration
	// CAFile is a PEM file of extra certificate authorities to trust for this
	// server only.
	CAFile string
}

// httpClient builds the client for the endpoint: bounded, never following a
// redirect (a password must not be replayed to another location), and, when a
// CA file is given, trusting it in addition to the system roots. There is no
// way to switch certificate verification off.
func (e Endpoint) httpClient() (*http.Client, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = config.DefaultRequestTimeout
	}

	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	transport, err := transportFor(e.CAFile)
	if err != nil {
		return nil, err
	}
	client.Transport = transport

	return client, nil
}

// transportFor returns a transport that trusts the system roots plus the CA
// file, or nil (the default transport) when there is no file.
func transportFor(caFile string) (http.RoundTripper, error) {
	if caFile == "" {
		return nil, nil
	}

	pem, err := os.ReadFile(caFile) // #nosec G304 -- the CA file the person chose with --ca-file
	if err != nil {
		return nil, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("read the CA file %q: %w", caFile, err), "",
			"Check the path, or sign in again with --ca-file.")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("the CA file %q holds no PEM certificate", caFile), "",
			"Give a PEM file with the certificate of your server's CA.")
	}

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("the default HTTP transport is not an *http.Transport")
	}
	cloned := transport.Clone()
	cloned.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	return cloned, nil
}

// Ping checks that something answers HTTP at the endpoint. Any HTTP answer
// counts: it proves the address is reachable and speaks HTTP, and the sign-in
// that follows proves it is Machinarchy. A certificate problem is reported as
// such.
func Ping(ctx context.Context, e Endpoint) error {
	client, err := e.httpClient()
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.URL, "/")+"/healthz/ready", nil)
	if err != nil {
		return clierr.Usage(fmt.Errorf("build request: %w", err))
	}
	request.Header.Set("User-Agent", defaultUserAgent())

	response, err := client.Do(request)
	if err != nil {
		return connectError(e.URL, err)
	}
	_ = response.Body.Close()

	return nil
}

// connectError classifies a failure to reach a server, naming certificate
// problems as such so the person is pointed at --ca-file.
func connectError(server string, err error) error {
	var urlErr interface{ Unwrap() error }
	cause := err
	if errors.As(err, &urlErr) && urlErr.Unwrap() != nil {
		cause = urlErr.Unwrap()
	}

	var unknownAuthority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &unknownAuthority) || errors.As(err, &invalid) || errors.As(err, &hostname) {
		return clierr.Detailed(clierr.ExitUnavailable,
			fmt.Errorf("the certificate of %s is not trusted (%s)", server, certificateReason(err)),
			"the server's certificate was not signed by an authority this computer trusts, or does not match its name",
			"If this is your own server, give the CA that signed it:\n  machinarchy login "+server+" --ca-file ca.pem")
	}

	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return clierr.Detailed(clierr.ExitUnavailable, fmt.Errorf("%s did not answer in time", server), "",
			"Check the address and that the server is running, or raise --request-timeout.")
	}

	return clierr.Detailed(clierr.ExitUnavailable, fmt.Errorf("cannot reach %s: %w", server, cause), "",
		"Check the address and that the server is running.")
}

func certificateReason(err error) string {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	switch {
	case errors.As(err, &unknownAuthority):
		return "certificate signed by unknown authority"
	case errors.As(err, &hostname):
		return "the certificate does not match the address"
	default:
		return "the certificate is not valid"
	}
}

// User is the account a credential belongs to.
type User struct {
	ID       int    `json:"id" yaml:"id"`
	Username string `json:"username" yaml:"username"`
	Email    string `json:"email" yaml:"email"`
}

// Label returns the name to show for the user.
func (u User) Label() string {
	if u.Username != "" {
		return u.Username
	}

	return u.Email
}

// LoginResult is what a successful sign-in returns.
type LoginResult struct {
	// Token is the session token. It is a secret.
	Token string
	User  User
}

// Login exchanges an email address or username and a password for a login
// session (POST /api/auth/login). The password goes only in the request body.
func Login(ctx context.Context, e Endpoint, identifier, password string) (*LoginResult, error) {
	client, err := e.httpClient()
	if err != nil {
		return nil, err
	}

	body := map[string]string{"password": password}
	if strings.Contains(identifier, "@") {
		body["email"] = identifier
	} else {
		body["username"] = identifier
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	path := "/api/auth/login"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.URL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, clierr.Usage(fmt.Errorf("build login request: %w", err))
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", defaultUserAgent())

	response, err := client.Do(request)
	if err != nil {
		return nil, connectError(e.URL, err)
	}
	defer func() { _ = response.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read login response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, newError(response, http.MethodPost, path, data)
	}

	var reply struct {
		Token string `json:"token"`
		User  User   `json:"user"`
	}
	if err := json.Unmarshal(data, &reply); err != nil || reply.Token == "" {
		return nil, clierr.Detailed(clierr.ExitUnavailable,
			errors.New("that address did not answer like a Machinarchy server"), invalidResponseHint, "Check the address.")
	}

	return &LoginResult{Token: reply.Token, User: reply.User}, nil
}

// Session is one login session of the signed-in user.
type Session struct {
	ID        string    `json:"id" yaml:"id"`
	Current   bool      `json:"current" yaml:"current"`
	CreatedAt time.Time `json:"createdAt" yaml:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt" yaml:"expiresAt"`
	ClientIP  string    `json:"clientIp" yaml:"clientIp"`
	UserAgent string    `json:"userAgent" yaml:"userAgent"`
}

// AuthClient is the part of the API that identifies the caller and ends
// sessions. HTTPClient implements it; tests fake it.
type AuthClient interface {
	// Me returns the account of the credential (GET /api/v1/me). It accepts a
	// session and an API token alike.
	Me(ctx context.Context) (*User, error)
	// Sessions lists the caller's login sessions (GET /api/v1/sessions). It
	// needs a login session.
	Sessions(ctx context.Context) ([]Session, error)
	// Logout ends the session the request was made with (POST /api/auth/logout).
	Logout(ctx context.Context) error
	// RevokeSessions ends every login session of the caller
	// (DELETE /api/v1/sessions). API tokens are not affected.
	RevokeSessions(ctx context.Context) error
}

var _ AuthClient = (*HTTPClient)(nil)

// Me returns the account the credential belongs to.
func (c *HTTPClient) Me(ctx context.Context) (*User, error) {
	var user User
	if err := c.get(ctx, "/api/v1/me", &user); err != nil {
		return nil, err
	}

	return &user, nil
}

// Sessions lists the caller's login sessions.
func (c *HTTPClient) Sessions(ctx context.Context) ([]Session, error) {
	var reply struct {
		Items []Session `json:"items"`
	}
	if err := c.get(ctx, "/api/v1/sessions", &reply); err != nil {
		return nil, err
	}

	return reply.Items, nil
}

// Logout ends the session the request was made with.
func (c *HTTPClient) Logout(ctx context.Context) error {
	return c.request(ctx, http.MethodPost, "/api/auth/logout", nil, nil)
}

// RevokeSessions ends every login session of the caller.
func (c *HTTPClient) RevokeSessions(ctx context.Context) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/sessions", nil, nil)
}

package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/spf13/pflag"
)

// IOStreams keeps command input, output, and diagnostics independently injectable.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// APIClientBuilder creates the narrow API client used by a command.
type APIClientBuilder func(config.Target) (api.Client, error)

// Factory is the command dependency boundary. Commands receive it explicitly
// instead of reaching into package globals or constructing clients themselves.
type Factory struct {
	Streams       IOStreams
	Logger        *slog.Logger
	ClientBuilder APIClientBuilder
	config        *config.Store
}

// LoadConfig reads the selected configuration and binds the root flags.
func (f *Factory) LoadConfig(path string, flags *pflag.FlagSet) error {
	store, err := config.Load(path, flags)
	if err != nil {
		return err
	}
	f.config = store
	level := slog.LevelInfo
	if store.Verbose() {
		level = slog.LevelDebug
	}
	f.Logger = slog.New(slog.NewJSONHandler(f.Streams.ErrOut,
		&slog.HandlerOptions{Level: level}))
	return nil
}

// Config returns the loaded context configuration.
func (f *Factory) Config() *config.Store {
	if f == nil {
		return nil
	}
	return f.config
}

// NewAPIClient resolves the effective context and constructs its API client.
func (f *Factory) NewAPIClient(ctx context.Context) (api.Client, error) {
	if f == nil || f.config == nil {
		return nil, errors.New("CLI configuration is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("command context is required")
	}
	target, err := f.config.Resolve()
	if err != nil {
		return nil, err
	}
	builder := f.ClientBuilder
	if builder == nil {
		builder = func(target config.Target) (api.Client, error) {
			return api.NewHTTPClient(target)
		}
	}
	client, err := builder(target)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("API client builder returned a nil client")
	}
	return client, nil
}

package cred

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/fsutil"
)

const (
	fileVersion = 2
	maxFileSize = 1 << 20
)

// File is the credential store backed by one YAML file, mode 0600 in a 0700
// directory, written atomically.
type File struct {
	path string
}

var _ Store = (*File)(nil)

// NewFile returns a store for the file at path. The file need not exist.
func NewFile(path string) *File { return &File{path: path} }

type document struct {
	Version int              `yaml:"version"`
	Servers map[string]Entry `yaml:"servers,omitempty"`
}

// Where returns the file path.
func (f *File) Where() string { return "file " + f.path }

// Get returns the credentials saved for server.
func (f *File) Get(server string) (Entry, error) {
	doc, err := f.read()
	if err != nil {
		return Entry{}, err
	}

	return doc.Servers[server], nil
}

// Put saves c in the slot of its kind.
func (f *File) Put(server string, c Credential) error {
	if server == "" || c.Secret == "" {
		return errors.New("a credential needs a server and a secret")
	}

	doc, err := f.read()
	if err != nil {
		return err
	}

	entry := doc.Servers[server]
	saved := c
	switch SlotOf(c.Kind) {
	case SlotSession:
		entry.Session = &saved
	case SlotToken:
		entry.Token = &saved
	}
	doc.Servers[server] = entry

	return f.write(doc)
}

// Delete removes the given slots of server, or both when none is given.
func (f *File) Delete(server string, slots ...Slot) error {
	doc, err := f.read()
	if err != nil {
		return err
	}

	entry, ok := doc.Servers[server]
	if !ok {
		return nil
	}
	if len(slots) == 0 {
		slots = []Slot{SlotSession, SlotToken}
	}
	for _, slot := range slots {
		switch slot {
		case SlotSession:
			entry.Session = nil
		case SlotToken:
			entry.Token = nil
		}
	}

	if entry.Empty() {
		delete(doc.Servers, server)
	} else {
		doc.Servers[server] = entry
	}

	return f.write(doc)
}

// read loads the file. A missing file is an empty store. A file that other
// users can read, that is not valid, or that is newer than this CLI stops the
// command: the CLI never falls back to something less safe.
func (f *File) read() (document, error) {
	empty := document{Version: fileVersion, Servers: map[string]Entry{}}

	file, err := os.Open(f.path) // #nosec G304 -- the operator-selected credentials file
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}

		return empty, f.problem(fmt.Errorf("read credentials %q: %w", f.path, err), "Check the file's permissions, or move it aside and log in again.")
	}
	defer func() { _ = file.Close() }() // read-only: a close error cannot lose data

	info, err := file.Stat()
	if err != nil {
		return empty, f.problem(fmt.Errorf("read credentials %q: %w", f.path, err), "")
	}
	if info.IsDir() {
		return empty, f.problem(fmt.Errorf("credentials %q is a directory", f.path), "Move it aside and log in again.")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return empty, clierr.Detailed(clierr.ExitUsage,
			fmt.Errorf("the credentials file %s can be read by other users (mode %04o)", f.path, info.Mode().Perm()),
			"it holds your tokens, and the CLI will not use a file that others can read",
			"chmod 600 "+f.path)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return empty, f.problem(fmt.Errorf("read credentials %q: %w", f.path, err), "")
	}
	if len(data) > maxFileSize {
		return empty, f.problem(fmt.Errorf("credentials %q exceeds the maximum supported size", f.path), "Move it aside and log in again.")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty, nil
	}

	var doc document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return empty, f.problem(fmt.Errorf("parse credentials %q: %w", f.path, err), "Move the file aside and log in again.")
	}
	if doc.Version != fileVersion {
		return empty, f.problem(fmt.Errorf("credentials %q has version %d; this CLI reads version %d", f.path, doc.Version, fileVersion),
			"Use the CLI that wrote it, or move the file aside and log in again.")
	}
	if doc.Servers == nil {
		doc.Servers = map[string]Entry{}
	}

	return doc, nil
}

func (f *File) write(doc document) error {
	doc.Version = fileVersion
	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}

	return fsutil.WriteAtomic(f.path, data, 0o700, 0o600)
}

func (f *File) problem(err error, next string) error {
	return clierr.Detailed(clierr.ExitUsage, err, "", next)
}

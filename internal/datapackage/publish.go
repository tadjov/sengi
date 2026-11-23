package datapackage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const DefaultVersion = "v1"

// PublishOptions locates a package being published; StagingDir and ParentDir must share a filesystem.
type PublishOptions struct {
	StagingDir string
	ParentDir  string
	Version    string // empty means DefaultVersion
}

// Published is a package in its version folder; Existing means it was already there,
// and Restored lists what this run wrote into it.
type Published struct {
	Dir      string
	Package  Package
	Existing bool
	Restored Restored
}

// Restored names, by path, the resources a rerun wrote into an existing version: Missing ones
// its folder lacked, Mismatched ones whose bytes contradicted its descriptor.
type Restored struct {
	Missing    []string
	Mismatched []string
}

// Any reports whether the rerun wrote anything.
func (r Restored) Any() bool {
	return len(r.Missing)+len(r.Mismatched) > 0
}

// VersionConflictError reports a version already published from other inputs or with other bytes.
type VersionConflictError struct {
	Dir     string
	Version string
	// Differences lists each resource or lock entry that differs, published value first.
	Differences []string
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version %s at %s already holds other content (%s); publish under another version",
		e.Version, e.Dir, strings.Join(e.Differences, "; "))
}

// Publish moves a staged package to ParentDir/<version>. An existing version with the
// same resource hashes, and the same preprocess lock inputs, is kept, any resource missing
// from it or holding other bytes restored; any other is a *VersionConflictError.
func Publish(ctx context.Context, pkg Package, opts PublishOptions) (Published, error) {
	if opts.Version == "" {
		opts.Version = DefaultVersion
	}
	if err := checkPublishable(pkg, opts); err != nil {
		return Published{}, err
	}
	pkg.Version = opts.Version
	staged, err := Write(ctx, opts.StagingDir, pkg)
	if err != nil {
		return Published{}, err
	}
	target := filepath.Join(opts.ParentDir, opts.Version)
	if existing, ok, err := openPublished(ctx, target); err != nil || ok {
		if err != nil {
			return Published{}, err
		}
		return keepPublished(ctx, opts.StagingDir, existing, staged)
	}
	if err := os.MkdirAll(opts.ParentDir, 0o755); err != nil {
		return Published{}, fmt.Errorf("create %s: %w", opts.ParentDir, err)
	}
	if err := os.Rename(opts.StagingDir, target); err != nil {
		// Another producer may have published this version meanwhile.
		if existing, ok, openErr := openPublished(ctx, target); openErr == nil && ok {
			return keepPublished(ctx, opts.StagingDir, existing, staged)
		}
		return Published{}, fmt.Errorf("publish %s as %s: %w", opts.StagingDir, target, err)
	}
	return Published{Dir: target, Package: staged}, nil
}

func checkPublishable(pkg Package, opts PublishOptions) error {
	if pkg.Version != "" {
		return fmt.Errorf("publish package %s: version %q is set on the package; pass it in PublishOptions", pkg.Name, pkg.Version)
	}
	if err := validateResourcePath(opts.Version); err != nil || strings.Contains(opts.Version, "/") {
		return fmt.Errorf("publish package %s: version %q must be a single folder name", pkg.Name, opts.Version)
	}
	if opts.StagingDir == "" || opts.ParentDir == "" {
		return errors.New("publish needs both a staging and a parent folder")
	}
	staging, err := filepath.Abs(opts.StagingDir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", opts.StagingDir, err)
	}
	parent, err := filepath.Abs(opts.ParentDir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", opts.ParentDir, err)
	}
	if rel, err := filepath.Rel(staging, parent); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("publish: parent %s lies inside staging %s, which is removed or moved", opts.ParentDir, opts.StagingDir)
	}
	return nil
}

func openPublished(ctx context.Context, dir string) (Published, bool, error) {
	if _, err := os.Stat(filepath.Join(dir, DescriptorName)); errors.Is(err, fs.ErrNotExist) {
		return Published{}, false, nil
	}
	opened, err := Open(ctx, dir)
	if err != nil {
		return Published{}, false, fmt.Errorf("open published %s: %w", dir, err)
	}
	return Published{Dir: dir, Package: opened.Package, Existing: true}, true, nil
}

func keepPublished(ctx context.Context, staging string, existing Published, staged Package) (Published, error) {
	if differences := contentDifferences(existing.Package, staged); len(differences) > 0 {
		if err := os.RemoveAll(staging); err != nil {
			return Published{}, fmt.Errorf("remove staging %s: %w", staging, err)
		}
		return Published{}, &VersionConflictError{Dir: existing.Dir, Version: staged.Version, Differences: differences}
	}
	restored, err := restoreResources(ctx, staging, existing, staged)
	if err != nil {
		return Published{}, err
	}
	if err := os.RemoveAll(staging); err != nil {
		return Published{}, fmt.Errorf("remove staging %s: %w", staging, err)
	}
	existing.Restored = restored
	return existing, nil
}

// contentDifferences ignores the tool in a preprocess lock, so a newer build may rerun a version.
func contentDifferences(published, staged Package) []string {
	var differences []string
	publishedHashes := make(map[string]string, len(published.Resources))
	for _, r := range published.Resources {
		publishedHashes[r.Name] = r.Hash
	}
	for _, r := range staged.Resources {
		if hash, ok := publishedHashes[r.Name]; !ok || hash != r.Hash {
			differences = append(differences, fmt.Sprintf("resource %s: %s published, %s staged", r.Name, orNone(hash), r.Hash))
		}
		delete(publishedHashes, r.Name)
	}
	for name, hash := range publishedHashes {
		differences = append(differences, fmt.Sprintf("resource %s: %s published, none staged", name, hash))
	}
	var publishedLock, stagedLock Lock
	if published.Preprocess != nil {
		publishedLock = published.Preprocess.Lock
	}
	if staged.Preprocess != nil {
		stagedLock = staged.Preprocess.Lock
	}
	for _, entry := range []struct{ name, published, staged string }{
		{"spec", publishedLock.Spec, stagedLock.Spec},
		{"mapping", publishedLock.Mapping, stagedLock.Mapping},
		{"input", publishedLock.Input, stagedLock.Input},
	} {
		if entry.published != entry.staged {
			differences = append(differences, fmt.Sprintf("preprocess %s: %s published, %s staged", entry.name, orNone(entry.published), orNone(entry.staged)))
		}
	}
	slices.Sort(differences)
	return differences
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// restoreResources moves the staged file in for each resource a version whose descriptor is on
// disk lacks, as in a clone that tracks descriptors but not payloads, or holds with other bytes.
func restoreResources(ctx context.Context, staging string, existing Published, staged Package) (Restored, error) {
	var restored Restored
	for _, r := range staged.Resources {
		target := filepath.Join(existing.Dir, filepath.FromSlash(r.Path))
		state, err := publishedState(ctx, target, r)
		if err != nil {
			return Restored{}, err
		}
		if state == resourceIntact {
			continue
		}
		if err := os.Rename(filepath.Join(staging, filepath.FromSlash(r.Path)), target); err != nil {
			return Restored{}, fmt.Errorf("restore published resource %s: %w", target, err)
		}
		if state == resourceMissing {
			restored.Missing = append(restored.Missing, r.Path)
		} else {
			restored.Mismatched = append(restored.Mismatched, r.Path)
		}
	}
	return restored, nil
}

type resourceState int

const (
	resourceIntact resourceState = iota
	resourceMissing
	resourceMismatched
)

// publishedState compares the file at path with r, by size and then by hash.
func publishedState(ctx context.Context, path string, r Resource) (resourceState, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return resourceMissing, nil
	}
	if err != nil {
		return 0, fmt.Errorf("check published resource %s: %w", path, err)
	}
	if info.Size() != r.Bytes {
		return resourceMismatched, nil
	}
	held, err := digestFile(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("hash published resource %s: %w", path, err)
	}
	if held.hash != r.Hash {
		return resourceMismatched, nil
	}
	return resourceIntact, nil
}

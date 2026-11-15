package meta

import "path/filepath"

// TrackRelative writes path, absolute or relative to the working directory,
// relative to trackDir, so an artifact reads the same from any checkout. A path
// outside the track climbs out of it: ../shared/suite.yaml.
func TrackRelative(trackDir, path string) string {
	if path == "" {
		return ""
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	base, err := filepath.Abs(trackDir)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(withoutSymlinks(base), withoutSymlinks(target))
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// ResolveFromTrack returns the file a recorded path names: a relative one
// starts at trackDir, an absolute one, as older artifacts recorded, stands.
func ResolveFromTrack(trackDir, recorded string) string {
	if recorded == "" || filepath.IsAbs(recorded) {
		return recorded
	}
	return filepath.Join(trackDir, filepath.FromSlash(recorded))
}

// withoutSymlinks keeps macOS's /var and /private/var from reading as two
// directories. A path not written yet resolves through its nearest existing
// folder.
func withoutSymlinks(path string) string {
	missing := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, missing)
		}
		if filepath.Dir(dir) == dir {
			return path
		}
		missing = filepath.Join(filepath.Base(dir), missing)
	}
}

package auth

import (
	"os"
)

type authFileSnapshot struct {
	path   string
	digest string
}

// SetSourceFileSnapshot binds an uncommitted file observation to its canonical content.
// Check it under the auth mutation lock so an older queued scan cannot overwrite
// a management save committed after the scan read the file.
func (a *Auth) SetSourceFileSnapshot(path string, data []byte) {
	if a != nil && path != "" {
		a.sourceFileSnapshot = &authFileSnapshot{path: path, digest: canonicalAuthFileDigest(data)}
	}
}

func (a *Auth) sourceFileSnapshotCurrent() bool {
	if a == nil || a.sourceFileSnapshot == nil {
		return true
	}
	data, err := os.ReadFile(a.sourceFileSnapshot.path)
	return err == nil && canonicalAuthFileDigest(data) == a.sourceFileSnapshot.digest
}

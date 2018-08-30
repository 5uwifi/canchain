package version

import (
	"fmt"
)

const (
	VersionMajor = 0
	VersionMinor = 3
	VersionPatch = 4
	VersionMeta  = "unstable"
)

var Version = func() string {
	return fmt.Sprintf("%d.%d.%d", VersionMajor, VersionMinor, VersionPatch)
}()

var VersionWithMeta = func() string {
	v := Version
	if VersionMeta != "" {
		v += "-" + VersionMeta
	}
	return v
}()

func ArchiveVersion(gitCommit string) string {
	vsn := Version
	if VersionMeta != "stable" {
		vsn += "-" + VersionMeta
	}
	if len(gitCommit) >= 8 {
		vsn += "-" + gitCommit[:8]
	}
	return vsn
}

func VersionWithCommit(gitCommit string) string {
	vsn := Version
	if len(gitCommit) >= 8 {
		vsn += "-" + gitCommit[:8]
	}
	return vsn
}

package content

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Item names, versions, formats and source volumes come from whoever publishes, possibly on
// another host, and end up as file names in this host's pools. They are only ever used
// through these checks: one file directly in the pool, never a path out of it.

// FileName is a version's file in a pool: content-<item>-v<version>.<format>.
func FileName(item, version, format string) (string, error) {
	return poolFile(fmt.Sprintf("content-%s-v%s.%s", item, version, format))
}

// poolFile returns f if it names a file directly in a pool.
func poolFile(f string) (string, error) {
	if !filepath.IsLocal(f) || filepath.Base(f) != f || strings.ContainsAny(f, "\\\x00") {
		return "", invalidf("%q cannot be the name of a file in a pool", f)
	}
	return f, nil
}

// VolFile is a volume's file name in its pool.
func VolFile(name string) (string, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "\\\x00") {
		return "", invalidf("%q is not a volume of a pool", name)
	}
	return name, nil
}

// checkFiles refuses an item whose versions could not be stored as files of a pool.
func checkFiles(name string, versions []ItemVersion) error {
	for _, v := range versions {
		if _, err := FileName(name, v.Version, v.Format); err != nil {
			return err
		}
		if v.SourceVol != "" {
			if _, err := VolFile(v.SourceVol); err != nil {
				return err
			}
		}
	}
	return nil
}

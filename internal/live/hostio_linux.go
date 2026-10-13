package live

import "os"

func defaultHostIO() func() (hostCounters, error) {
	root := os.DirFS("/")
	return func() (hostCounters, error) { return readHostCounters(root) }
}

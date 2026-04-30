package policy

import "os"

// readFile is split out so we can stub it in tests without an indirect
// dependency on os. It also keeps policy.go free of the `os` import,
// which makes the public API surface there easier to read.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

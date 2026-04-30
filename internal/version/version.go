// Package version exposes build-time identity for the ws1 binary.
//
// Variables here are populated via -ldflags at link time, e.g.:
//
//	go build -ldflags "-X github.com/zhangxuyang/ws1-uem-agent/internal/version.Version=0.1.0 \
//	                   -X github.com/zhangxuyang/ws1-uem-agent/internal/version.Commit=$(git rev-parse HEAD) \
//	                   -X github.com/zhangxuyang/ws1-uem-agent/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
//	  ./cmd/ws1
//
// Defaults below are intentionally generic so unstamped dev builds are
// distinguishable from real releases.
package version

// Version is the semantic version of this binary. Stamped at build time.
var Version = "0.0.0-dev"

// Commit is the git commit hash that produced this binary. Stamped at build time.
var Commit = "unknown"

// BuildDate is the UTC RFC3339 timestamp of the build. Stamped at build time.
var BuildDate = "unknown"

// SpecVersion is the WS1 API Explorer version that the compiled bindings are
// derived from. Stamped at build time from spec/VERSION.
var SpecVersion = "unknown"

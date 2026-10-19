package version

// Current is the CLI version. Set via ldflags at build time:
// -X github.com/cafaye/cafe/internal/version.Current=1.2.3
// Left empty, it defaults to "dev".
var Current = "dev"
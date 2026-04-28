module github.com/davorobilinovic/claude-deployable

go 1.25.0

require github.com/modelcontextprotocol/go-sdk v1.5.0

require (
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)

// The Cowork sandbox proxy allowlists github.com but not golang.org's
// vanity-redirect host.  These replaces resolve golang.org/x/* through
// the upstream-mirrored github.com/golang/* repos, which the proxy does
// allow.  Forks that build outside the sandbox can drop these.
replace (
	golang.org/x/crypto => github.com/golang/crypto v0.50.0
	golang.org/x/mod => github.com/golang/mod v0.35.0
	golang.org/x/net => github.com/golang/net v0.53.0
	golang.org/x/oauth2 => github.com/golang/oauth2 v0.36.0
	golang.org/x/sync => github.com/golang/sync v0.20.0
	golang.org/x/sys => github.com/golang/sys v0.43.0
	golang.org/x/term => github.com/golang/term v0.42.0
	golang.org/x/text => github.com/golang/text v0.36.0
	golang.org/x/tools => github.com/golang/tools v0.44.0
)

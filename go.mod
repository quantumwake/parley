module github.com/quantumwake/statefs.ai

go 1.25.0

require github.com/quantumwake/statefs v0.5.4

// Local development against the sibling checkout while the client
// additions (CreateNamespaceWith, Head, FindNamespacesByName) are unreleased.
replace github.com/quantumwake/statefs => ../statefs

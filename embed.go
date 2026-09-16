// Package dist carries the embedded launcher and its contracts for `spec-audit skill install|update`.
// Data only: the tool package materialises the installed copy.
package dist

import "embed"

// Files holds skills/spec-audit and the contracts SKILL.md links to; dot-files are excluded by go:embed.
//
//go:embed skills/spec-audit docs/accepted-index.md docs/legacy-extraction.md docs/php-sdk-contract.md
var Files embed.FS

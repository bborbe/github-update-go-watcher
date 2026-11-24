// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// IgnoreSurfaceFiles is the ordered list of repo files the stale-ignore pass
// reads from a repo's default branch. A missing file is an empty surface, not
// an error. Makefile and Makefile.precommit both carry the VULNCHECK_IGNORE
// assignment; .trivyignore and .osv-scanner.toml each carry their own format.
var IgnoreSurfaceFiles = []string{
	"Makefile",
	"Makefile.precommit",
	".trivyignore",
	".osv-scanner.toml",
}

// advisoryIDPattern matches the three advisory ID families the fleet's ignore
// surfaces carry: GO-YYYY-NNNN, CVE-YYYY-NNNN and GHSA-xxxx-xxxx-xxxx.
// Anchored on word boundaries and length-bounded so an attacker-controlled
// ignore file cannot make the parser allocate unboundedly.
var advisoryIDPattern = regexp.MustCompile(
	`\b(GO-\d{4}-\d{4,}|CVE-\d{4}-\d{4,}|GHSA(-[0-9a-z]{4}){3})\b`,
)

// vulncheckAssignmentPattern matches a VULNCHECK_IGNORE assignment line after
// trimming: the literal name, optional whitespace, an optional `?`, then `=`.
var vulncheckAssignmentPattern = regexp.MustCompile(`^VULNCHECK_IGNORE\s*\??=`)

// osvScannerIDPattern matches an `id = "<value>"` line in a TOML block.
var osvScannerIDPattern = regexp.MustCompile(`^id\s*=\s*"([^"]*)"$`)

// ExtractAdvisoryIDs returns every advisory ID in text, de-duplicated, in
// first-seen order. It returns nil when text carries none.
func ExtractAdvisoryIDs(text string) []string {
	return dedupeIDs(advisoryIDPattern.FindAllString(text, -1))
}

// ParseIgnoreSurface parses one ignore surface file by its path, returning the
// advisory IDs it declares. An unknown path, or an absent file, returns nil.
// Makefile and Makefile.precommit are parsed as VULNCHECK_IGNORE assignments,
// .trivyignore line-wise, .osv-scanner.toml as [[IgnoredVulns]] blocks.
func ParseIgnoreSurface(path string, content []byte) []string {
	switch path {
	case "Makefile", "Makefile.precommit":
		return ParseVulncheckIgnore(content)
	case ".trivyignore":
		return ParseTrivyIgnore(content)
	case ".osv-scanner.toml":
		return ParseOsvScannerToml(content)
	default:
		return nil
	}
}

// ParseVulncheckIgnore returns the advisory IDs in the first VULNCHECK_IGNORE
// assignment in a Makefile-style file, including a `\`-continued multi-line
// assignment. The assignment ends at the first line not ending in `\`.
func ParseVulncheckIgnore(content []byte) []string {
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		loc := vulncheckAssignmentPattern.FindStringIndex(trimmed)
		if loc == nil {
			continue
		}
		value := strings.TrimSpace(trimmed[loc[1]:])
		for strings.HasSuffix(value, `\`) && i+1 < len(lines) {
			value = strings.TrimSpace(strings.TrimSuffix(value, `\`))
			i++
			value = value + " " + strings.TrimSpace(lines[i])
		}
		return ExtractAdvisoryIDs(value)
	}
	return nil
}

// ParseTrivyIgnore returns the advisory IDs in a .trivyignore file: one ID per
// line, with `#` comment lines, blank lines and trailing `# …` remarks ignored.
func ParseTrivyIgnore(content []byte) []string {
	var ids []string
	for _, line := range strings.Split(string(content), "\n") {
		before, _, _ := strings.Cut(line, "#")
		trimmed := strings.TrimSpace(before)
		if trimmed == "" {
			continue
		}
		ids = append(ids, ExtractAdvisoryIDs(trimmed)...)
	}
	return dedupeIDs(ids)
}

// ParseOsvScannerToml returns the advisory IDs declared by `id = "<ID>"` lines
// inside [[IgnoredVulns]] blocks of a .osv-scanner.toml file, and nothing else.
func ParseOsvScannerToml(content []byte) []string {
	var ids []string
	inIgnoredVulns := false
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[") {
			inIgnoredVulns = trimmed == "[[IgnoredVulns]]"
			continue
		}
		if !inIgnoredVulns {
			continue
		}
		match := osvScannerIDPattern.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		ids = append(ids, ExtractAdvisoryIDs(match[1])...)
	}
	return dedupeIDs(ids)
}

// dedupeIDs removes duplicate IDs while preserving first-seen order. It
// returns nil when ids is empty.
func dedupeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

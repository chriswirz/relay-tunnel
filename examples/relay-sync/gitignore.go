package main

// A focused .gitignore matcher used to exclude ignored files from the sync
// manifest on both sides. It supports the patterns found in the vast majority
// of real .gitignore files: comments and blank lines, "!" negation, a leading
// "/" to anchor to the .gitignore's directory, a trailing "/" for directories
// only, the "*", "?" and "**" wildcards, and nested .gitignore files (rules
// from a deeper directory override shallower ones). Within a file, a later
// matching line wins, matching Git.
//
// It does not implement every corner of Git's semantics (for example escaped
// trailing spaces or character classes like "[a-z]"); those are rare in
// practice. The ".git" directory is always skipped, independent of any rule.

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// ignoreRule is one compiled .gitignore line, scoped to the directory that
// contained the .gitignore (base, relative to the sync root, slash-separated).
type ignoreRule struct {
	base     string
	negate   bool
	dirOnly  bool
	anchored bool
	re       *regexp.Regexp
}

// loadIgnoreRules reads a .gitignore in dirAbs (whose path relative to the sync
// root is baseRel) and compiles its rules. A missing file yields no rules.
func loadIgnoreRules(dirAbs, baseRel string) ([]ignoreRule, error) {
	f, err := os.Open(filepath.Join(dirAbs, ".gitignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var rules []ignoreRule
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if r, ok := compileIgnoreLine(sc.Text(), baseRel); ok {
			rules = append(rules, r)
		}
	}
	return rules, sc.Err()
}

func compileIgnoreLine(line, base string) (ignoreRule, bool) {
	line = strings.TrimRight(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	line = strings.TrimRight(line, " ")
	if line == "" {
		return ignoreRule{}, false
	}

	r := ignoreRule{base: base}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	}
	// An escaped leading "#" or "!" is a literal.
	if strings.HasPrefix(line, "\\#") || strings.HasPrefix(line, "\\!") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	// A slash anywhere other than a trailing one anchors the pattern to base.
	if strings.Contains(line, "/") {
		r.anchored = true
	}
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return ignoreRule{}, false
	}
	r.re = regexp.MustCompile(globToRegex(line))
	return r, true
}

// globToRegex converts a gitignore glob into an anchored regexp source. "*" and
// "?" do not cross "/"; "**" does.
func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				if i+2 < len(glob) && glob[i+2] == '/' {
					b.WriteString("(?:.*/)?") // "**/" => zero or more segments
					i += 3
					continue
				}
				b.WriteString(".*") // trailing or embedded "**"
				i += 2
				continue
			}
			b.WriteString("[^/]*")
			i++
		case '?':
			b.WriteString("[^/]")
			i++
		case '/':
			b.WriteByte('/')
			i++
		default:
			if strings.IndexByte(`.+()|^$[]{}\`, c) >= 0 {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}

// matches reports whether rel (relative to the sync root, slash-separated)
// matches this rule.
func (r ignoreRule) matches(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	sub := rel
	if r.base != "" {
		if !strings.HasPrefix(rel, r.base+"/") {
			return false
		}
		sub = rel[len(r.base)+1:]
	}
	if r.anchored {
		return r.re.MatchString(sub)
	}
	// A non-anchored pattern matches at any depth: test the basename.
	return r.re.MatchString(path.Base(sub))
}

// isIgnored applies the rules in order (shallow to deep, and top to bottom
// within a file); the last matching rule wins.
func isIgnored(rules []ignoreRule, rel string, isDir bool) bool {
	ignored := false
	for _, r := range rules {
		if r.matches(rel, isDir) {
			ignored = !r.negate
		}
	}
	return ignored
}

package main

// Telling English sentences apart from mistyped terminal commands.

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

//go:embed shell/function-words.txt
var functionWordsText string

// Webster's Second International (public domain), for systems without a
// word list such as Windows.
//
//go:embed shell/words.txt.gz
var bundledDict []byte

var dictPaths = []string{"/usr/share/dict/words", "/usr/dict/words"}

// Python's idea of whitespace (str.isspace, and \s in its regexes): the
// classifier was first written in Python and its answers are kept.
func isSpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

func strip(text string) string { return strings.TrimFunc(text, isSpace) }

func split(text string) []string { return strings.FieldsFunc(text, isSpace) }

// Very common English words; a hit on one of these is a strong English
// signal.
var functionWords = func() map[string]bool {
	words := map[string]bool{}
	for _, line := range strings.Split(functionWordsText, "\n") {
		if w := strip(line); w != "" {
			words[w] = true
		}
	}
	return words
}()

var (
	wordRE = regexp.MustCompile(`^[A-Za-z]+(?:'[A-Za-z]+)?$`)
	// Numbers in a sentence: 2, 1,000, 3.5, 10%, 2nd, 5pm, 90s.
	numberRE = regexp.MustCompile(`(?i)^[0-9]+(?:[.,:][0-9]+)*(?:%|st|nd|rd|th|s|am|pm)?$`)
	// IP addresses, optionally with a user, port or prefix length:
	// 10.0.0.1:8080, user@192.168.1.20, 10.0.0.0/24, ::1, fe80::1/64.
	addressRE = regexp2.MustCompile(
		`^(?:[A-Za-z0-9_.-]+@)?(?:[0-9]{1,3}(?:\.[0-9]{1,3}){3}(?::[0-9]+)?|(?=[0-9a-f:]*[0-9])[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7})(?:/[0-9]{1,3})?$`,
		regexp2.IgnoreCase)
	sentenceEndRE = regexp.MustCompile(`[?.!]+$`)
)

// The word list as one lowercase string, one word per line with a newline
// at each end. A line is checked with only a few lookups, which is much
// faster than building a set of half a million words first. Loaded on
// first use, so lines rejected as shell syntax never pay for it.
var dictionary = sync.OnceValue(func() string {
	var text []byte
	for _, file := range dictPaths {
		if data, err := os.ReadFile(file); err == nil {
			text = data
			break
		}
	}
	if text == nil {
		if r, err := gzip.NewReader(bytes.NewReader(bundledDict)); err == nil {
			text, _ = io.ReadAll(r)
		}
	}
	lower := asciiLower(text)
	// Normalizing every line is slow, so only do it when a list needs it.
	if strings.Contains(lower, " \n") || strings.Contains(lower, "\t\n") || strings.Contains(lower, "\r\n") {
		lower = trailingSpaceRE.ReplaceAllString(lower, "\n")
	}
	return "\n" + lower + "\n"
})

// asciiLower lowercases text in place when it is ASCII, as word lists
// usually are, which is several times faster than strings.ToLower.
func asciiLower(text []byte) string {
	for i, c := range text {
		switch {
		case c >= utf8.RuneSelf:
			return strings.ToLower(string(text))
		case 'A' <= c && c <= 'Z':
			text[i] = c + 'a' - 'A'
		}
	}
	return string(text)
}

var trailingSpaceRE = regexp.MustCompile(`[\t\r ]+\n`)

func inDictionary(word string) bool {
	return functionWords[word] || strings.Contains(dictionary(), "\n"+word+"\n")
}

var suffixes = [][2]string{
	{"ies", "y"}, {"es", ""}, {"s", ""}, {"ed", ""}, {"ed", "e"}, {"ing", ""}, {"ing", "e"}, {"ly", ""},
}

func isEnglishWord(word string) bool {
	w := strings.ToLower(word)
	if inDictionary(w) {
		return true
	}
	if stem, _, ok := strings.Cut(w, "'"); ok {
		return inDictionary(stem)
	}
	// Crude stemming for plurals and verb forms the word list lacks.
	for _, s := range suffixes {
		suffix, repl := s[0], s[1]
		if strings.HasSuffix(w, suffix) && len(w) > len(suffix)+1 && inDictionary(w[:len(w)-len(suffix)]+repl) {
			return true
		}
	}
	return false
}

// A port of Pygments' BashLexer (2.21), which the Python version used: a
// stack of states, each a list of rules tried in order at the current
// position. Python's \w, \d and \b are Unicode-aware, so they are spelled
// out with property escapes. The rules need lookbehind and
// backreferences, which Go's regexp lacks.
type lexRule struct {
	re    *regexp2.Regexp
	types []string
	next  string
}

type ruleOrInclude struct {
	rule    *lexRule
	include string
}

const (
	w      = `[\p{L}\p{N}_]`
	bStart = `(?<!` + w + `)` // \b before a word character
	bEnd   = `(?!` + w + `)`  // \b after a word character
	// regexp2 has its own escapes for code points.
	lexSpace = `\t-\r\x1c-\x20\x85\xa0  -     　`
	s        = `[` + lexSpace + `]`
)

func rule(source string, types any, next string, opts ...regexp2.RegexOptions) ruleOrInclude {
	var o regexp2.RegexOptions
	for _, opt := range opts {
		o |= opt
	}
	t, ok := types.([]string)
	if !ok {
		t = []string{types.(string)}
	}
	// \G anchors the match at the starting position, like a sticky regex.
	return ruleOrInclude{rule: &lexRule{re: regexp2.MustCompile(`\G(?:`+source+`)`, o), types: t, next: next}}
}

func include(state string) ruleOrInclude { return ruleOrInclude{include: state} }

var lexer = map[string][]ruleOrInclude{
	"root": {
		include("basic"),
		rule("`", "Literal.String.Backtick", "backticks"),
		include("data"),
		include("interp"),
	},
	"interp": {
		rule(`\$\(\(`, "Keyword", "math"),
		rule(`\$\(`, "Keyword", "paren"),
		rule(`\$\{#?`, "Literal.String.Interpol", "curly"),
		rule(`\$[a-zA-Z_]`+w+`*`, "Name.Variable", ""),
		rule(`\$(?:\p{Nd}+|[#$?!_*@-])`, "Name.Variable", ""),
		rule(`\$`, "Text", ""),
	},
	"basic": {
		rule(bStart+`(if|fi|else|while|in|do|done|for|then|return|function|case|select|break|continue|until|esac|elif)(?![\p{L}\p{N}_#$?!*@-])(`+s+`*)`,
			[]string{"Keyword", "Text.Whitespace"}, ""),
		rule(bStart+`(?:alias|bg|bind|builtin|caller|cd|command|compgen|complete|declare|dirs|disown|echo|enable|eval|exec|exit|export|false|fc|fg|getopts|hash|help|history|jobs|kill|let|local|logout|popd|printf|pushd|pwd|read|readonly|set|shift|shopt|source|suspend|test|time|times|trap|true|type|typeset|ulimit|umask|unalias|unset|wait)(?=[`+lexSpace+")`])",
			"Name.Builtin", ""),
		rule(`^#![^\n]+\n`, "Comment.Hashbang", ""),
		rule(`#[^\n]*\n`, "Comment.Single", ""),
		rule(`\\[\s\S]`, "Literal.String.Escape", ""),
		rule(`(`+bStart+w+`+)(`+s+`*)(\+?=)`, []string{"Name.Variable", "Text.Whitespace", "Operator"}, ""),
		rule(`[\[\]{}()=]`, "Operator", ""),
		rule(`<<<`, "Operator", ""),
		rule(`<<-?`+s+`*('?)\\?(`+w+`+)[\s\S]+?\2`, "Literal.String", ""),
		rule(`&&|\|\|`, "Operator", ""),
	},
	"data": {
		rule(`\$?"(\\.|[^"\\$])*"`, "Literal.String.Double", "", regexp2.Singleline),
		rule(`"`, "Literal.String.Double", "string"),
		rule(`\$'(\\\\|\\[0-7]+|\\.|[^'\\])*'`, "Literal.String.Single", "", regexp2.Singleline),
		rule(`'.*?'`, "Literal.String.Single", "", regexp2.Singleline),
		rule(`;`, "Punctuation", ""),
		rule(`&`, "Punctuation", ""),
		rule(`\|`, "Punctuation", ""),
		rule(s+`+`, "Text.Whitespace", ""),
		rule(`\p{Nd}+`+bEnd, "Literal.Number", ""),
		rule(`[^=`+lexSpace+`\[\]{}()$"'`+"`"+`\\<&|;]+`, "Text", ""),
		rule(`<`, "Text", ""),
	},
	"string": {
		rule(`"`, "Literal.String.Double", "#pop"),
		rule(`(\\\\|\\[0-7]+|\\.|[^"\\$])+`, "Literal.String.Double", "", regexp2.Singleline),
		include("interp"),
	},
	"curly": {
		rule(`\}`, "Literal.String.Interpol", "#pop"),
		rule(`:-`, "Keyword", ""),
		rule(w+`+`, "Name.Variable", ""),
		rule(`[^}:"'`+"`"+`$\\]+`, "Punctuation", ""),
		rule(`:`, "Punctuation", ""),
		include("root"),
	},
	"paren": {
		rule(`\)`, "Keyword", "#pop"),
		include("root"),
	},
	"math": {
		rule(`\)\)`, "Keyword", "#pop"),
		rule(`\*\*|\|\||<<|>>|[-+*/%^|&<>]`, "Operator", ""),
		rule(`\p{Nd}+#[\p{Nd}a-zA-Z]+`, "Literal.Number", ""),
		rule(`\p{Nd}+#(?! )`, "Literal.Number", ""),
		rule(`0[xX][\p{Nd}a-fA-F]+`, "Literal.Number", ""),
		rule(`\p{Nd}+`, "Literal.Number", ""),
		rule(`[a-zA-Z_]`+w+`*`, "Name.Variable", ""),
		include("root"),
	},
	"backticks": {
		rule("`", "Literal.String.Backtick", "#pop"),
		include("root"),
	},
}

func flatten(state string) []*lexRule {
	var rules []*lexRule
	for _, r := range lexer[state] {
		if r.include != "" {
			rules = append(rules, flatten(r.include)...)
		} else {
			rules = append(rules, r.rule)
		}
	}
	return rules
}

var states = func() map[string][]*lexRule {
	m := map[string][]*lexRule{}
	for state := range lexer {
		m[state] = flatten(state)
	}
	return m
}()

type token struct{ typ, value string }

var (
	newlinesRE     = regexp.MustCompile(`\r\n?`)
	edgeNewlinesRE = regexp.MustCompile(`^\n+|\n+$`)
)

func bashTokens(text string) []token {
	// Like Pygments: drop a BOM, normalize newlines, drop surrounding ones
	// and end with exactly one.
	text = strings.TrimPrefix(text, "\ufeff")
	text = edgeNewlinesRE.ReplaceAllString(newlinesRE.ReplaceAllString(text, "\n"), "") + "\n"
	runes := []rune(text)
	var tokens []token
	stack := []string{"root"}
	pos := 0
outer:
	for pos < len(runes) {
		for _, r := range states[stack[len(stack)-1]] {
			m, err := r.re.FindRunesMatchStartingAt(runes, pos)
			if err != nil || m == nil || m.Index != pos {
				continue
			}
			if len(r.types) > 1 {
				groups := m.Groups()
				for i, typ := range r.types {
					if v := groups[i+1].String(); v != "" {
						tokens = append(tokens, token{typ, v})
					}
				}
			} else if m.Length > 0 {
				tokens = append(tokens, token{r.types[0], m.String()})
			}
			pos += m.Length
			if r.next == "#pop" {
				if len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
			} else if r.next != "" {
				stack = append(stack, r.next)
			}
			continue outer
		}
		// No rule matched: a newline starts over at the root, anything else
		// is an error token.
		if runes[pos] == '\n' {
			stack = stack[:1]
			tokens = append(tokens, token{"Text.Whitespace", "\n"})
		} else {
			tokens = append(tokens, token{"Error", string(runes[pos])})
		}
		pos++
	}
	return tokens
}

func isA(typ, parent string) bool { return typ == parent || strings.HasPrefix(typ, parent+".") }

var (
	shellOperators   = map[string]bool{"|": true, "||": true, "&&": true, ">": true, ">>": true, "<": true, "&": true, "=": true}
	shellPunctuation = map[string]bool{";": true, "(": true, ")": true, "{": true, "}": true, "[": true, "]": true}
)

// shellSyntaxScore counts tokens that only make sense in shell syntax.
func shellSyntaxScore(line string) int {
	score := 0
	for _, t := range bashTokens(line) {
		v := strip(t.value)
		switch {
		case v == "":
		case isA(t.typ, "Name.Variable"), isA(t.typ, "Literal.String.Backtick"):
			score += 2
		case isA(t.typ, "Operator"):
			if shellOperators[v] {
				score += 2
			}
		case isA(t.typ, "Punctuation"):
			if shellPunctuation[v] {
				score++
			}
		case isA(t.typ, "Text"):
			if strings.HasPrefix(v, "-") || strings.ContainsAny(v, "/=*") || strings.HasPrefix(v, "~") {
				score += 2
			}
		}
	}
	return score
}

func looksLikeEnglish(line string) bool {
	text := strip(line)
	if text == "" {
		return false
	}
	// An address's dots, colons and slash are not shell syntax, so score
	// the line with each address read as a plain number.
	tokens := split(text)
	for i, t := range tokens {
		if ok, _ := addressRE.MatchString(strings.Trim(t, `,.?!;"`)); ok {
			tokens[i] = "0"
		}
	}
	if shellSyntaxScore(sentenceEndRE.ReplaceAllString(strings.Join(tokens, " "), "")) >= 2 {
		return false
	}

	var words, letters []string
	for _, t := range tokens {
		if word := strings.Trim(t, `,.?!:;"`); word != "" {
			words = append(words, word)
		}
	}
	if len(words) == 0 {
		return false
	}
	for _, word := range words {
		if wordRE.MatchString(word) {
			letters = append(letters, word)
		} else if !numberRE.MatchString(word) {
			return false
		}
	}

	endsLikeSentence := strings.ContainsAny(text[len(text)-1:], "?.!")
	if len(words) < 2 && !endsLikeSentence {
		// A single token is far more likely a mistyped command.
		switch strings.ToLower(words[0]) {
		case "hello", "hi", "hey", "thanks", "please":
			return true
		}
		return false
	}

	// Numbers say nothing either way, so judge the remaining words.
	if len(letters) == 0 {
		return false
	}
	english, hasFunctionWord := 0, false
	for _, word := range letters {
		if isEnglishWord(word) {
			english++
		}
		hasFunctionWord = hasFunctionWord || functionWords[strings.ToLower(word)]
	}
	ratio := float64(english) / float64(len(letters))
	if hasFunctionWord && ratio >= 0.6 {
		return true
	}
	return ratio >= 0.8 && (len(words) >= 3 || endsLikeSentence)
}

// classify decides whether line is English; with exists its first word is a
// real command.
func classify(line string, exists bool) bool {
	words := split(line)
	if len(words) == 0 {
		return false
	}
	// Some English words are real binaries (e.g. /usr/bin/what, which, who).
	// Only call it English when it is clearly a longer sentence.
	if exists {
		return functionWords[strings.ToLower(words[0])] && len(words) >= 4 && looksLikeEnglish(line)
	}
	return looksLikeEnglish(line)
}

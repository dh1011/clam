package main

import "testing"

// Expected answers were recorded from the Python version this replaced.
var english = []string{
	"what files are in this directory", "how do I list files", "can you summarize this repo",
	"tell me about this project", "show me the biggest files", "list the python files", "explain this repo",
	"hello", "thanks", "hi there!", "why?", "what's the weather like today?", "don't delete anything please",
	"the quick brown fox jumps over the lazy dog", "please explain git rebase", "is there a Makefile here?",
	"run the tests", "which files changed recently", "who wrote this code", "find the largest file in here",
	"where is the readme", "help me write a commit message", "time to go home", "  spaced   out   question here?  ",
	"what is 2 plus 2", "how do I list the 3 biggest files", "remind me in 10 minutes", "what happened in 2024?",
	"show the last 5 commits", "what is running on 192.168.1.10", "can you check if 10.0.0.1:8080 is reachable?",
	"is 10.0.0.0/24 a private range?", "who owns ::1 and fe80::1?", "why does 192.168.1.1 not respond",
	"can you set up ssh authentication with key for user@192.168.1.20",
}

var notEnglish = []string{
	"summarize README.md", "ls -la", "git status", "echo $HOME", "cat foo | grep bar", "make && make install", "x=1",
	"for i in 1 2 3; do echo $i; done", "`whoami`", "cd ~/projects", "rm -rf /tmp/x", "npm run build", "gti stauts",
	"sl", "pyhton script.py", "find . -name '*.go'", "ssh user@host", "docker ps -a", "kubectl get pods",
	"foo bar baz", "42", "1 2 3", "sleep 10", "head -n 5 file", "10.0.0.1", "curl 10.0.0.1/24 -v", "ssh user@192.168.1.20", "how many lines are in agent.mjs", "",
}

// Lines whose first word is a real command need a longer sentence.
var (
	englishWithCommand    = []string{"which files changed recently", "who wrote this code", "where is the readme", "what is going on here"}
	notEnglishWithCommand = []string{
		"find the largest file in here", "help me write a commit message", "time to go home", "find . -name x", "which python", "who",
	}
)

func TestReadsEnglishSentencesAsEnglish(t *testing.T) {
	for _, line := range english {
		if !classify(line, false) {
			t.Errorf("%q: want English", line)
		}
	}
}

func TestLeavesCommandsAndTyposAlone(t *testing.T) {
	for _, line := range notEnglish {
		if classify(line, false) {
			t.Errorf("%q: want not English", line)
		}
	}
}

func TestIsStricterWhenTheFirstWordIsACommand(t *testing.T) {
	for _, line := range englishWithCommand {
		if !classify(line, true) {
			t.Errorf("%q: want English", line)
		}
	}
	for _, line := range notEnglishWithCommand {
		if classify(line, true) {
			t.Errorf("%q: want not English", line)
		}
	}
}

func TestScoresShellSyntaxLikeThePygmentsBashLexer(t *testing.T) {
	for line, want := range map[string]int{
		"what files are here":  0,
		"echo $HOME":           2,
		"make && make install": 2,
		"ls -la /tmp":          4,
		"x=$(date) # comment":  4,
	} {
		if got := shellSyntaxScore(line); got != want {
			t.Errorf("shellSyntaxScore(%q) = %d, want %d", line, got, want)
		}
	}
}

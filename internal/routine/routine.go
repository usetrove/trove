// Package routine labels a prompt the user typed as routine (a short confirmation, "continue", "run the tests",
// "commit") or not, and says whether it reads as a correction of the previous reply ("no, that's wrong", "try
// again"). hook_routine_steer (technique B24) uses the first label to decide which prompts get a request to answer
// without deliberating, and the second to count re-prompts after a steered turn.
//
// The classifier is a pure, deterministic function of the text: the same prompt always gets the same label, so a
// session replayed or resumed is steered the same way. It runs in memory only. The prompt is never stored, logged or
// hashed; only the label and a fixed reason code leave this package (PRINCIPLES.md rule 7).
//
// It errs towards "not routine". A prompt is routine only when the whole of it, after normalising case, spacing and
// end punctuation, is made of phrases from a fixed list. Anything else, such as a qualifier ("yes but fix the race
// first"), a question, a path, code, a number, a second line or more than a few words, makes it not routine. A
// routine prompt that is wrongly left alone costs nothing; a real request wrongly steered could cost quality.
package routine

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reason codes. They are fixed words, safe to count and show; they never carry prompt text.
const (
	ReasonRoutine    = "routine"     // every part is a listed routine phrase
	ReasonEmpty      = "empty"       // nothing but spaces and punctuation
	ReasonTooLong    = "too_long"    // more than MaxBytes or MaxWords
	ReasonMultiline  = "multiline"   // more than one line
	ReasonQuestion   = "question"    // contains a question mark
	ReasonSymbols    = "symbols"     // code, paths, numbers or markup
	ReasonSlash      = "slash"       // a slash command or a path at the start
	ReasonCorrection = "correction"  // reads as a correction of the previous reply
	ReasonUnlisted   = "unlisted"    // some part is not a listed routine phrase
	ReasonNotText    = "not_text"    // invalid UTF-8
	ReasonOnlyPolite = "only_polite" // nothing but "please" or "thanks"
)

// Limits. A routine prompt is short; anything longer is a request with content.
const (
	MaxBytes = 80
	MaxWords = 8
)

// Label is the classifier's answer.
type Label struct {
	Routine bool
	// Correction is true when the prompt reads as a correction of the previous reply. A correction is never routine.
	Correction bool
	Reason     string
}

// Classify labels one prompt.
func Classify(prompt string) Label {
	if !utf8.ValidString(prompt) {
		return Label{Reason: ReasonNotText}
	}
	trimmed := strings.TrimSpace(prompt)
	if len(trimmed) > MaxBytes*4 {
		return Label{Reason: ReasonTooLong, Correction: isCorrection(normalise(firstLine(trimmed)))}
	}
	if strings.ContainsAny(trimmed, "\n\r") {
		return Label{Reason: ReasonMultiline, Correction: isCorrection(normalise(firstLine(trimmed)))}
	}
	norm := normalise(trimmed)
	corr := isCorrection(norm)
	switch {
	case corr:
		return Label{Correction: true, Reason: ReasonCorrection}
	case norm == "":
		return Label{Reason: ReasonEmpty}
	case strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!") || strings.HasPrefix(trimmed, "#"):
		return Label{Reason: ReasonSlash}
	case strings.Contains(trimmed, "?"):
		return Label{Reason: ReasonQuestion}
	case len(trimmed) > MaxBytes || len(strings.Fields(norm)) > MaxWords:
		return Label{Reason: ReasonTooLong}
	case hasSymbols(norm):
		return Label{Reason: ReasonSymbols}
	}
	words := strings.Fields(norm)
	words = dropPolite(words)
	if len(words) == 0 {
		return Label{Reason: ReasonOnlyPolite}
	}
	if covers(words) {
		return Label{Routine: true, Reason: ReasonRoutine}
	}
	return Label{Reason: ReasonUnlisted}
}

// normalise lower-cases, turns curly apostrophes straight, removes commas and the end punctuation that does not
// change meaning (. ! … and emoji-free trailing spaces), and collapses spaces. A question mark is kept so the caller
// can see it; symbols inside the text are kept so hasSymbols can see them.
func normalise(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("’", "'", "‘", "'", "…", ".", "—", " ", "–", " ").Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ',', ';':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.TrimRight(out, ".! ")
	out = strings.TrimLeft(out, ".! ")
	// "ok." "ok!" inside a combined prompt ("ok. continue") split into words cleanly.
	out = strings.NewReplacer(". ", " ", "! ", " ").Replace(out)
	return strings.Join(strings.Fields(out), " ")
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// hasSymbols reports code, paths, numbers or markup: anything but letters, spaces, apostrophes, hyphens, a question
// mark (handled by the caller) and the end punctuation normalise already removed.
func hasSymbols(s string) bool {
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII:
		case r == ' ' || r == '\'' || r == '-' || r == '?':
		default:
			return true
		}
	}
	return false
}

// politeWords may appear anywhere in a routine prompt and are ignored: "yes please", "thanks, continue".
var politeWords = map[string]bool{"please": true, "pls": true, "plz": true, "thanks": true, "thx": true, "ty": true}

func dropPolite(words []string) []string {
	out := words[:0:0]
	for i := 0; i < len(words); i++ {
		w := words[i]
		if politeWords[w] {
			continue
		}
		if w == "thank" && i+1 < len(words) && words[i+1] == "you" {
			i++
			continue
		}
		out = append(out, w)
	}
	return out
}

// phrases are the routine phrases, as word sequences. A prompt is routine when its words are a sequence of these
// and nothing else. "and" is only ever part of a listed phrase ("commit and push"), never a free joiner, so "run
// the tests and fix what fails" is not routine.
var phrases = buildPhrases(
	// Confirmations.
	"yes", "y", "yep", "yeah", "yea", "yup", "ya", "sure", "ok", "okay", "k", "kk", "alright", "all right",
	"right", "correct", "exactly", "agreed", "absolutely", "definitely", "of course", "indeed",
	"sounds good", "sounds great", "looks good", "looks great", "lgtm", "good", "great", "perfect", "nice",
	"cool", "awesome", "excellent", "fine", "that works", "works for me", "makes sense", "got it",
	"approved", "approve", "confirm", "confirmed", "accept", "accepted", "yes please", "go", "go ahead", "go for it",
	"do it", "do that", "do so", "let's do it", "lets do it", "let's go", "lets go", "ship it", "make it so",
	"proceed", "please proceed", "you can proceed", "you may proceed", "sure thing", "will do", "that's right",
	"thats right", "that's fine", "thats fine", "that's good", "thats good",
	// Continuations.
	"continue", "go on", "keep going", "carry on", "next", "next step", "resume", "onward", "onwards", "and continue",
	"continue please", "keep at it", "proceed with the plan", "continue with the plan", "go with the plan",
	"go with that", "go with it", "do the next step", "on to the next", "next one", "move on",
	// Running things.
	"run it", "run the tests", "run tests", "run the test", "run test", "run the test suite", "run the suite",
	"run all tests", "run all the tests", "test it", "rerun the tests", "re-run the tests", "rerun tests",
	"re-run tests", "run the tests again", "run tests again", "rerun", "re-run", "run it again",
	"build", "build it", "run the build", "rebuild", "compile", "compile it", "lint", "lint it", "run lint",
	"run the linter", "run the linters", "format", "format it", "run the formatter", "typecheck", "type check",
	"run typecheck", "run the type checker", "check it", "verify it", "run the checks", "run checks",
	// Version control.
	"commit", "commit it", "commit this", "commit that", "commit the changes", "commit changes", "commit them",
	"commit and push", "commit it and push", "commit and push it", "push", "push it", "push the changes",
	"push changes", "stage it", "stage the changes", "make a commit", "open a pr", "open the pr", "create a pr",
	"create the pr", "merge", "merge it",
)

// maxPhraseWords is the length of the longest phrase.
var maxPhraseWords int

func buildPhrases(list ...string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, p := range list {
		w := strings.Fields(p)
		if len(w) > maxPhraseWords {
			maxPhraseWords = len(w)
		}
		m[strings.Join(w, " ")] = true
	}
	return m
}

// covers reports whether words split, left to right, into listed phrases. Longest match first, with backtracking,
// so "commit and push" is read as one phrase and "yes go ahead" as two.
func covers(words []string) bool {
	if len(words) == 0 {
		return true
	}
	for n := min(maxPhraseWords, len(words)); n >= 1; n-- {
		if phrases[strings.Join(words[:n], " ")] && covers(words[n:]) {
			return true
		}
	}
	return false
}

// correctionStarts are openings that make a prompt a correction of the previous reply, as whole words at the start.
var correctionStarts = []string{
	"no", "nope", "nah", "wrong", "incorrect", "not quite", "not right", "not what", "that's not", "thats not",
	"that is not", "that's wrong", "thats wrong", "that is wrong", "this is wrong", "this is not", "this isn't",
	"that isn't", "that didn't", "that did not", "it didn't", "it did not", "it doesn't", "it does not",
	"still", "try again", "again", "undo", "revert", "roll back", "rollback", "you forgot", "you missed",
	"you broke", "you didn't", "you did not", "you misunderstood", "i said", "i asked", "i meant", "i told you",
	"why did you", "stop", "wait", "hold on", "actually", "don't", "do not",
}

// correctionAnywhere are phrases that mark a correction wherever they appear.
var correctionAnywhere = []string{
	"didn't work", "did not work", "doesn't work", "does not work", "still fails", "still failing", "still broken",
	"still wrong", "still not", "not what i", "that's wrong", "thats wrong", "that is wrong", "you broke",
	"broke the", "try again", "that was wrong", "not working", "you forgot", "you missed",
}

// isCorrection reads a normalised prompt as a correction of the previous reply. It is the re-prompt signal: after a
// steered turn, a correction counts against the steer.
func isCorrection(norm string) bool {
	if norm == "" {
		return false
	}
	for _, p := range correctionStarts {
		if norm == p || strings.HasPrefix(norm, p+" ") || strings.HasPrefix(norm, p+"?") {
			return true
		}
	}
	for _, p := range correctionAnywhere {
		if containsWords(norm, p) {
			return true
		}
	}
	return false
}

// containsWords reports whether phrase occurs in s on word boundaries.
func containsWords(s, phrase string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], phrase)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(phrase)
		if (start == 0 || s[start-1] == ' ') && (end == len(s) || !isWordByte(s[end])) {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '\'' || b == '-' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

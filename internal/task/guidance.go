package task

import "regexp"

// askNote goes after every message to the agent, the same words for every
// agent: ask when something that matters is unclear, instead of guessing.
const askNote = `

(From forgerun: if something important in this request is unclear, such as which file, language, or behaviour is meant, don't guess and don't change any files. Reply with your questions under a "## Questions" heading, and stop. Otherwise, go ahead.)`

// planNote goes after a plan-only message instead.
const planNote = `

(From forgerun: plan only. Don't change any files. Read what you need, then reply with a plan in Markdown: what you'll change and why, which files, and how you'll check it works. If something important is unclear, add your questions under a "## Questions" heading. The user will approve the plan or ask for changes.)`

// withGuidance is the message the agent gets for a turn.
func withGuidance(prompt string, plan bool) string {
	if plan {
		return prompt + planNote
	}
	return prompt + askNote
}

var questionsHeading = regexp.MustCompile(`(?im)^\s{0,3}#{1,6}\s*(clarifying\s+)?questions?\s*:?\s*$`)

// asksQuestions reports whether an agent's reply asks the user something
// before it goes on: it has a "## Questions" section.
func asksQuestions(reply string) bool {
	return questionsHeading.MatchString(reply)
}

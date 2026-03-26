package protocol

type TextMessageRequest struct {
	Text                   string
	ChatID                 int
	Model                  string
	WithPotentialQuestions bool
}

type TextCompletionResult struct {
	Content            string
	ChatModel          string
	PotentialQuestions []string
}

type TextStreamEvent struct {
	Type      string
	ChatModel string
	Delta     string
	FinalText string
}

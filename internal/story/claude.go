package story

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is the model Claude narrates with unless told otherwise.
const DefaultModel = "claude-opus-5-5"

// promptVersion names the instructions and the request's shape. Bump it
// whenever either changes, so that cached narrations are written anew.
const promptVersion = "story-1"

// instructions are the fixed part of every request, cached by the API.
const instructions = `You narrate one chapter of a Go repository's history. The history was cut into chapters where the work shifted; each chapter's facts were computed from the repository's commits, and a template has already turned them into a plain baseline narration.

You get a JSON object with:
- "chapter": the chapter's facts. Dates are RFC 3339 timestamps. Package names are directory paths, "." is the root package. "Moments" are the chapter's key moments, each with a one-line "Text" that states facts. Fields not described here are facts too.
- "previous_chapter_titles": the titles of the chapters before this one, so the story reads as one; it may be empty.
- "baseline": the template's title and paragraph for this chapter. Every statement in it is true.

Write a title and a paragraph for the chapter:
- Title: at most 6 words, sentence case (capitalize only the first word and names that the facts contain), no final period. Don't repeat an earlier chapter's title.
- Paragraph: 2 to 4 sentences of plain, warm, unhurried prose for a developer who knows the codebase. Tell what happened in the chapter and where the work went.

State only what the facts contain:
- Every number you write must appear in the facts or the baseline, written with digits. Don't compute new numbers, percentages or durations.
- Write package, file and symbol names exactly as the facts spell them, and name no others.
- Name no people unless the facts name them.
- Don't invent motives, intentions, goals, causes or outcomes ("to improve performance", "the team decided"), and don't describe the code beyond the facts.
- No hyperbole and no exclamation marks: avoid words like "massive", "dramatic", "epic", "explosive".

When it is unclear whether something is supported by the facts, leave it out. Plain and accurate is better than vivid.`

// schema is the shape of the model's answer.
var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title": map[string]any{"type": "string", "description": "at most 6 words"},
		"text":  map[string]any{"type": "string", "description": "2 to 4 sentences"},
	},
	"required":             []string{"title", "text"},
	"additionalProperties": false,
}

// fallbackModels can be served by another model when they refuse, through
// the API's server-side fallbacks.
var fallbackModels = map[string]bool{
	"claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true,
}

// Claude narrates chapters with a Claude model through the Anthropic
// Messages API. It starts from the templated narration, checks the model's
// prose against the facts, asks once more when the check fails, and falls
// back to the template when the model fails again or cannot be reached.
// Narrations the check accepts are cached on disk.
type Claude struct {
	Model string
	// Cache is the directory accepted narrations are kept in, by a hash of
	// the request and the model; empty for none.
	Cache string
	// Log, if set, reports the chapters that fell back to the template.
	Log func(format string, args ...any)

	client anthropic.Client
}

// NewClaude returns a narrator using model, or DefaultModel if empty. The
// client reads ANTHROPIC_API_KEY from the environment; opts override it.
func NewClaude(model, cache string, opts ...option.RequestOption) *Claude {
	if model == "" {
		model = DefaultModel
	}
	return &Claude{Model: model, Cache: cache, client: anthropic.NewClient(opts...)}
}

// request is what the model is told about one chapter.
type request struct {
	Chapter  Facts     `json:"chapter"`
	Previous []string  `json:"previous_chapter_titles"`
	Baseline narration `json:"baseline"`
}

type narration struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Narrate implements Narrator. It returns an error only when ctx ends.
func (c *Claude) Narrate(ctx context.Context, f Facts) (Narration, error) {
	base, _ := Templates{}.Narrate(ctx, f)
	req := request{Chapter: f, Previous: f.Previous, Baseline: narration{base.Title, base.Text}}
	if req.Previous == nil {
		req.Previous = []string{}
	}
	body, err := json.MarshalIndent(req, "", " ")
	if err != nil {
		return base, err
	}
	key := c.key(body)
	if n, ok := c.cached(key); ok {
		return n, nil
	}

	prompt := string(body)
	var problems []string
	for attempt := 0; attempt < 2; attempt++ {
		n, err := c.call(ctx, prompt)
		if ctx.Err() != nil {
			return base, ctx.Err()
		}
		if err != nil {
			problems = []string{err.Error()}
			break // the API failed; asking again will not help
		}
		problems, err = validate(f, base, n)
		if err != nil {
			problems = []string{err.Error()}
			break
		}
		if len(problems) == 0 {
			c.store(key, n)
			return n, nil
		}
		prompt = retryPrompt(body, n, problems)
	}
	c.logf("chapter %d of %d fell back to template narration: %s", f.Number, f.Of, strings.Join(problems, "; "))
	return base, nil
}

// retryPrompt asks the model to fix its draft.
func retryPrompt(body []byte, draft Narration, problems []string) string {
	d, _ := json.Marshal(narration{draft.Title, draft.Text})
	var b strings.Builder
	b.Write(body)
	b.WriteString("\n\nAn earlier answer for this chapter was rejected:\n")
	b.Write(d)
	b.WriteString("\n\nIt broke these rules:\n")
	for _, p := range problems {
		b.WriteString("- " + p + "\n")
	}
	b.WriteString("\nWrite the title and paragraph again, following every rule.")
	return b.String()
}

// call sends one request and returns the model's narration.
func (c *Claude) call(ctx context.Context, prompt string) (Narration, error) {
	params := anthropic.MessageNewParams{
		Model:     c.Model,
		MaxTokens: 8000,
		System: []anthropic.TextBlockParam{{
			Text:         instructions,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
	}
	var opts []option.RequestOption
	if fallbackModels[c.Model] {
		opts = append(opts,
			option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
			option.WithJSONSet("fallbacks", "default"))
	}
	resp, err := c.client.Messages.New(ctx, params, opts...)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return Narration{}, fmt.Errorf("the API answered %d", apiErr.StatusCode)
		}
		return Narration{}, err
	}
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn:
	case anthropic.StopReasonRefusal:
		return Narration{}, errors.New("the model declined")
	default:
		return Narration{}, fmt.Errorf("the model stopped: %s", resp.StopReason)
	}
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			var n narration
			if err := json.Unmarshal([]byte(t.Text), &n); err != nil {
				return Narration{}, fmt.Errorf("the answer is not JSON: %v", err)
			}
			return Narration{Title: strings.TrimSpace(n.Title), Text: strings.TrimSpace(n.Text), Model: string(resp.Model)}, nil
		}
	}
	return Narration{}, errors.New("the answer has no text")
}

func (c *Claude) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// key hashes what decides a narration: the prompt's version, the model and
// the request, which holds the facts.
func (c *Claude) key(body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", promptVersion, c.Model)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Claude) path(key string) string {
	return filepath.Join(c.Cache, "narrations", key+".json")
}

func (c *Claude) cached(key string) (Narration, bool) {
	if c.Cache == "" {
		return Narration{}, false
	}
	raw, err := os.ReadFile(c.path(key))
	if err != nil {
		return Narration{}, false
	}
	var n Narration
	if json.Unmarshal(raw, &n) != nil || n.Title == "" || n.Text == "" {
		return Narration{}, false
	}
	return n, true
}

// store caches n. A narration that cannot be cached is narrated again next
// time, so failures are only logged.
func (c *Claude) store(key string, n Narration) {
	if c.Cache == "" {
		return
	}
	raw, _ := json.Marshal(n)
	p := c.path(key)
	err := os.MkdirAll(filepath.Dir(p), 0o755)
	if err == nil {
		var tmp *os.File
		if tmp, err = os.CreateTemp(filepath.Dir(p), key+"-*.tmp"); err == nil {
			_, err = tmp.Write(raw)
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Rename(tmp.Name(), p)
			}
			if err != nil {
				os.Remove(tmp.Name())
			}
		}
	}
	if err != nil {
		c.logf("caching a narration: %v", err)
	}
}

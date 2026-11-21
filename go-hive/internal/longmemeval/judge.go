package longmemeval

// End-to-end accuracy grading with LongMemEval's own per-type judge prompts.
//
// Parity: tests/e2e/longmemeval.py::judge
// Parity: tests/e2e/longmemeval.py::judge_prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The judge every published LongMemEval score uses, with the official
// script's settings (src/evaluation/evaluate_qa.py: temperature 0, 10 tokens).
const (
	OfficialJudgeModel     = "gpt-4o-2024-08-06"
	OfficialJudgeMaxTokens = 10
)

// Verbatim from LongMemEval's src/evaluation/evaluate_qa.py.
const judgeStandardPrefix = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response " +
	"contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or " +
	"contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only " +
	"contains a subset of the information required by the answer, answer no. "

const judgeAnswerTail = "\n\nQuestion: {}\n\nCorrect Answer: {}\n\nModel Response: {}\n\nIs the model response correct? Answer yes or no only."

const judgeTemporalExtra = "In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of " +
	"days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), " +
	"the model's response is still correct. "

const judgeKnowledgeUpdatePrefix = "I will give you a question, a correct answer, and a response from a model. Please answer yes if " +
	"the response contains the correct answer. Otherwise, answer no. If the response contains some previous " +
	"information along with an updated answer, the response should be considered as correct as long as the updated " +
	"answer is the required answer."

const judgePreferencePrefix = "I will give you a question, a rubric for desired personalized response, and a " +
	"response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. " +
	"The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls " +
	"and utilizes the user's personal information correctly.\n\nQuestion: {}\n\nRubric: {}\n\nModel Response: {}\n\n" +
	"Is the model response correct? Answer yes or no only."

const judgeAbstentionPrompt = "I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the " +
	"model correctly identifies the question as unanswerable. The model could say that the information is incomplete, " +
	"or some other information is given but the asked information is not.\n\nQuestion: {}\n\nExplanation: {}\n\nModel " +
	"Response: {}\n\nDoes the model correctly identify the question as unanswerable? Answer yes or no only."

var judgeTemplates = map[string]string{
	"single-session-user":       judgeStandardPrefix + judgeAnswerTail,
	"single-session-assistant":  judgeStandardPrefix + judgeAnswerTail,
	"multi-session":             judgeStandardPrefix + judgeAnswerTail,
	"temporal-reasoning":        judgeStandardPrefix + judgeTemporalExtra + judgeAnswerTail,
	"knowledge-update":          judgeKnowledgeUpdatePrefix + judgeAnswerTail,
	"single-session-preference": judgePreferencePrefix,
}

// JudgePrompt renders the official grading prompt for one item.
//
// Parity: tests/e2e/longmemeval.py::judge_prompt
func JudgePrompt(item Item, response string) (string, error) {
	template, ok := judgeTemplates[item.QuestionType]
	if IsAbstention(item.QuestionID) {
		template, ok = judgeAbstentionPrompt, true
	}
	if !ok {
		return "", fmt.Errorf("question %s: no judge prompt for type %q", item.QuestionID, item.QuestionType)
	}
	return fillTemplate(template, item.Question, item.Answer.Text(), response), nil
}

// fillTemplate substitutes the template's positional {} placeholders in
// order, mirroring Python str.format for the official prompts.
func fillTemplate(template string, values ...string) string {
	var out strings.Builder
	rest := template
	next := 0
	for {
		index := strings.Index(rest, "{}")
		if index < 0 {
			out.WriteString(rest)
			return out.String()
		}
		out.WriteString(rest[:index])
		if next < len(values) {
			out.WriteString(values[next])
			next++
		} else {
			out.WriteString("{}")
		}
		rest = rest[index+2:]
	}
}

// Verdict is one judge ruling on one answer.
type Verdict struct {
	Correct    bool
	Raw        string
	JudgeModel string
}

// Message is one chat-completions message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// APIClient calls an OpenAI-compatible /chat/completions endpoint.
//
// Parity: tests/e2e/longmemeval.py::_chat
type APIClient struct {
	BaseURL     string
	APIKey      string
	HTTP        *http.Client
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	// Sleep overrides time.Sleep (tests use it to avoid real backoff waits).
	Sleep func(ctx context.Context, d time.Duration) error
}

// NewAPIClient builds a client with upstream's retry envelope (8 attempts,
// 2s doubling to a 60s cap).
func NewAPIClient(baseURL, apiKey string, httpClient *http.Client) *APIClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 300 * time.Second}
	}
	return &APIClient{
		BaseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:      apiKey,
		HTTP:        httpClient,
		MaxAttempts: 8,
		BaseDelay:   2 * time.Second,
		MaxDelay:    60 * time.Second,
	}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat performs one chat completion, retrying rate limits and transient
// server errors, and dropping `temperature` once when the endpoint rejects it
// (reasoning models), exactly as upstream._chat does.
func (c *APIClient) Chat(ctx context.Context, model string, messages []Message, temperature *float64, maxTokens *int) (string, error) {
	if c == nil || c.BaseURL == "" {
		return "", errors.New("judge endpoint is not configured")
	}
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = 8
	}
	delay := c.BaseDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	maxDelay := c.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 60 * time.Second
	}
	body := chatRequest{Model: model, Messages: messages, Temperature: temperature, MaxTokens: maxTokens}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(encoded))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if attempt == attempts-1 {
				break
			}
			if err := c.sleep(ctx, delay); err != nil {
				return "", err
			}
			delay = nextDelay(delay, maxDelay)
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt == attempts-1 {
				break
			}
			if err := c.sleep(ctx, delay); err != nil {
				return "", err
			}
			delay = nextDelay(delay, maxDelay)
			continue
		}
		if resp.StatusCode == http.StatusBadRequest && body.Temperature != nil && strings.Contains(string(payload), "temperature") {
			// Reasoning models reject temperature; retry without it.
			body.Temperature = nil
			continue
		}
		if retryableStatus(resp.StatusCode) && attempt < attempts-1 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(payload))
			if err := c.sleep(ctx, delay); err != nil {
				return "", err
			}
			delay = nextDelay(delay, maxDelay)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("chat completion: HTTP %d: %s", resp.StatusCode, snippet(payload))
		}
		var parsed chatResponse
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return "", fmt.Errorf("decode chat completion: %w", err)
		}
		if parsed.Error != nil && parsed.Error.Message != "" {
			return "", fmt.Errorf("chat completion: %s", parsed.Error.Message)
		}
		if len(parsed.Choices) == 0 {
			return "", errors.New("chat completion returned no choices")
		}
		content := ""
		if parsed.Choices[0].Message.Content != nil {
			content = *parsed.Choices[0].Message.Content
		}
		return strings.TrimSpace(content), nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("chat completion failed after %d attempts: %w", attempts, lastErr)
	}
	return "", fmt.Errorf("chat completion failed after %d attempts", attempts)
}

func (c *APIClient) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func nextDelay(delay, maxDelay time.Duration) time.Duration {
	next := delay * 2
	if next > maxDelay {
		next = maxDelay
	}
	return next
}

func snippet(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// Judge grades responses with one endpoint and one model.
type Judge struct {
	Client    *APIClient
	Model     string
	MaxTokens *int
}

// NewJudge builds a judge. The official model keeps the official 10-token
// cap; every other model is left room to answer.
func NewJudge(client *APIClient, model string) *Judge {
	judge := &Judge{Client: client, Model: model}
	if model == OfficialJudgeModel {
		maxTokens := OfficialJudgeMaxTokens
		judge.MaxTokens = &maxTokens
	}
	return judge
}

// Grade asks the judge about one answer. `'yes'` in the reply, lowercased, is
// the official rule.
//
// Parity: tests/e2e/longmemeval.py::judge
func (j *Judge) Grade(ctx context.Context, item Item, response string) (Verdict, error) {
	prompt, err := JudgePrompt(item, response)
	if err != nil {
		return Verdict{}, err
	}
	temperature := 0.0
	raw, err := j.Client.Chat(ctx, j.Model, []Message{{Role: "user", Content: prompt}}, &temperature, j.MaxTokens)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{Correct: strings.Contains(strings.ToLower(raw), "yes"), Raw: raw, JudgeModel: j.Model}, nil
}

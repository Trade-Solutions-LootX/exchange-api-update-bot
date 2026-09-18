package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"exchangebot/internal/httpx"
)

const clickupBase = "https://api.clickup.com/api/v2"

// ClickUp is the tiny REST v2 client the analyzer needs: create a task in one
// list and look for an open task with the same name (dedup).
type ClickUp struct {
	token  string
	listID string
	tag    string
	http   *httpx.Client
}

// Task is the subset of a ClickUp task we use.
type Task struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Text is the plain-text description (ClickUp returns both `description`
	// and `text_content`; the latter is the rendered plain text).
	Text string `json:"text_content"`
}

// NewClickUp builds a client; token goes into the Authorization header as-is.
func NewClickUp(token, listID, tag string, hc *httpx.Client) *ClickUp {
	return &ClickUp{token: token, listID: listID, tag: tag, http: hc}
}

func (c *ClickUp) headers() map[string]string {
	return map[string]string{"Authorization": c.token, "Content-Type": "application/json"}
}

// CreateTask files a task with a markdown description. priority: 1 urgent,
// 2 high, 3 normal, 4 low.
func (c *ClickUp) CreateTask(ctx context.Context, name, markdown string, priority int) (*Task, error) {
	payload := map[string]any{
		"name":                 name,
		"markdown_description": markdown,
		"tags":                 []string{c.tag},
		"priority":             priority,
	}
	body, _ := json.Marshal(payload)
	resp, err := c.http.Post(ctx, fmt.Sprintf("%s/list/%s/task", clickupBase, c.listID), body, c.headers())
	if err != nil {
		return nil, fmt.Errorf("clickup create: %w", err)
	}
	var t Task
	if err := json.Unmarshal(resp, &t); err != nil {
		return nil, fmt.Errorf("clickup decode: %w", err)
	}
	if t.ID == "" {
		return nil, fmt.Errorf("clickup: no task id in response")
	}
	return &t, nil
}

// ListOpenTagged returns the open tasks in the list carrying our tag, newest
// first (first page, 100 max — the dedup only needs recent ones).
func (c *ClickUp) ListOpenTagged(ctx context.Context) ([]Task, error) {
	q := url.Values{}
	q.Set("include_closed", "false")
	q.Set("subtasks", "false")
	q.Set("order_by", "created")
	q.Set("reverse", "true")
	q.Add("tags[]", c.tag)
	resp, err := c.http.Get(ctx, fmt.Sprintf("%s/list/%s/task?%s", clickupBase, c.listID, q.Encode()), c.headers())
	if err != nil {
		return nil, fmt.Errorf("clickup list: %w", err)
	}
	var out struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, fmt.Errorf("clickup decode: %w", err)
	}
	return out.Tasks, nil
}

// FindOpenByName returns an open task in the list carrying our tag whose name
// matches (case-insensitive), or nil.
func (c *ClickUp) FindOpenByName(ctx context.Context, name string) (*Task, error) {
	tasks, err := c.ListOpenTagged(ctx)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for i := range tasks {
		if strings.ToLower(strings.TrimSpace(tasks[i].Name)) == want {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

// AddComment appends a plain-text comment to a task.
func (c *ClickUp) AddComment(ctx context.Context, taskID, text string) error {
	body, _ := json.Marshal(map[string]any{"comment_text": text, "notify_all": false})
	if _, err := c.http.Post(ctx, fmt.Sprintf("%s/task/%s/comment", clickupBase, url.PathEscape(taskID)), body, c.headers()); err != nil {
		return fmt.Errorf("clickup comment: %w", err)
	}
	return nil
}

// Probe checks the token + list (used at startup so a bad token is loud).
func (c *ClickUp) Probe(ctx context.Context) (string, error) {
	resp, err := c.http.Get(ctx, fmt.Sprintf("%s/list/%s", clickupBase, c.listID), c.headers())
	if err != nil {
		return "", err
	}
	var l struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp, &l); err != nil {
		return "", err
	}
	return l.Name, nil
}

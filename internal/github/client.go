// Package github is a minimal GitHub REST/GraphQL client. It works against
// api.github.com with a token, or against the Sprites connector gateway with no
// token at all (the gateway signs requests for the calling sprite).
package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

// New reads PRREVIEW_GITHUB_BASE (default https://api.github.com) and GH_TOKEN
// (optional; leave empty when going through the Sprites gateway).
func New() *Client {
	base := os.Getenv("PRREVIEW_GITHUB_BASE")
	if base == "" {
		base = "https://api.github.com"
	}
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: os.Getenv("GH_TOKEN"),
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	User   struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

func (c *Client) do(method, path, accept string, in any, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "pr-review")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, Body: string(raw), Path: path}
	}
	switch o := out.(type) {
	case nil:
	case *string:
		*o = string(raw)
	default:
		return json.Unmarshal(raw, out)
	}
	return nil
}

type HTTPError struct {
	Status int
	Body   string
	Path   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("github %s: %d %s", e.Path, e.Status, truncate(e.Body, 300))
}

func IsNotFound(err error) bool {
	he, ok := err.(*HTTPError)
	return ok && he.Status == 404
}

func (c *Client) GetPR(owner, repo string, n int) (*PR, error) {
	var pr PR
	err := c.do("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, n), "", nil, &pr)
	return &pr, err
}

// Diff returns the full unified diff of the PR.
func (c *Client) Diff(owner, repo string, n int) (string, error) {
	var s string
	err := c.do("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, n), "application/vnd.github.v3.diff", nil, &s)
	return s, err
}

// Compare returns the unified diff between two commits (used for round >= 2).
func (c *Client) Compare(owner, repo, base, head string) (string, error) {
	var s string
	err := c.do("GET", fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, base, head), "application/vnd.github.v3.diff", nil, &s)
	return s, err
}

// GetFile returns a file's content at ref, or "" and nil if it does not exist.
func (c *Client) GetFile(owner, repo, path, ref string) (string, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	err := c.do("GET", fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref), "", nil, &out)
	if IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if out.Encoding != "base64" {
		return out.Content, nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	return string(b), err
}

type ReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Side      string `json:"side"`
	Body      string `json:"body"`
}

// CreateReview posts one review with inline comments. event is always COMMENT:
// the bot never approves or blocks.
func (c *Client) CreateReview(owner, repo string, n int, commit, body string, comments []ReviewComment) error {
	in := map[string]any{
		"commit_id": commit,
		"body":      body,
		"event":     "COMMENT",
		"comments":  comments,
	}
	return c.do("POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, n), "", in, nil)
}

func (c *Client) IssueComment(owner, repo string, n int, body string) error {
	return c.do("POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, n), "", map[string]string{"body": body}, nil)
}

func (c *Client) ReplyToReviewComment(owner, repo string, n int, commentID int64, body string) error {
	return c.do("POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies", owner, repo, n, commentID), "", map[string]string{"body": body}, nil)
}

type Thread struct {
	ID         string
	IsResolved bool
	Path       string
	Line       int
	Comments   []ThreadComment
}

type ThreadComment struct {
	DatabaseID int64
	Author     string
	Body       string
}

// ReviewThreads lists review threads via GraphQL (REST has no thread concept).
func (c *Client) ReviewThreads(owner, repo string, n int) ([]Thread, error) {
	q := `query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){
	  reviewThreads(first:100){nodes{id isResolved path line comments(first:50){nodes{databaseId author{login} body}}}}}}}`
	var out struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Path       string `json:"path"`
							Line       int    `json:"line"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
									Author     struct {
										Login string `json:"login"`
									} `json:"author"`
									Body string `json:"body"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	in := map[string]any{"query": q, "variables": map[string]any{"o": owner, "r": repo, "n": n}}
	if err := c.do("POST", "/graphql", "", in, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", out.Errors[0].Message)
	}
	var threads []Thread
	for _, node := range out.Data.Repository.PullRequest.ReviewThreads.Nodes {
		t := Thread{ID: node.ID, IsResolved: node.IsResolved, Path: node.Path, Line: node.Line}
		for _, cm := range node.Comments.Nodes {
			t.Comments = append(t.Comments, ThreadComment{DatabaseID: cm.DatabaseID, Author: cm.Author.Login, Body: cm.Body})
		}
		threads = append(threads, t)
	}
	return threads, nil
}

func (c *Client) ResolveThread(threadID string) error {
	q := `mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}`
	in := map[string]any{"query": q, "variables": map[string]any{"id": threadID}}
	return c.do("POST", "/graphql", "", in, nil)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

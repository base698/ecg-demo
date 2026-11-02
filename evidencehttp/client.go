package evidencehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/base698/ecg-demo/evidence"
)

type Client struct {
	endpoint, token string
	http            *http.Client
}
type ClientOptions struct {
	Token     string
	Transport http.RoundTripper
	Timeout   time.Duration
}

func NewClient(endpoint string, o ClientOptions) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid evidence endpoint")
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), token: o.Token, http: &http.Client{Transport: o.Transport, Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) call(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	method := http.MethodPost
	if input == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("evidence delivery: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return fmt.Errorf("evidence endpoint returned %d", res.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(output)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	return err
}
func (c *Client) Record(ctx context.Context, o evidence.Observation) error {
	if err := o.Validate(); err != nil {
		return err
	}
	return c.call(ctx, "/v1/observations", o, nil)
}
func (c *Client) Evaluate(ctx context.Context, requirements []evidence.Requirement) ([]evidence.Report, error) {
	var result EvaluationResponse
	err := c.call(ctx, "/v1/evaluate", EvaluationRequest{requirements}, &result)
	return result.Reports, err
}

// Series lists observed partitions. Declare requirements with Evaluate to find
// scenarios that have never been observed.
func (c *Client) Series(ctx context.Context) ([]evidence.Report, error) {
	var result EvaluationResponse
	err := c.call(ctx, "/v1/series", nil, &result)
	return result.Reports, err
}

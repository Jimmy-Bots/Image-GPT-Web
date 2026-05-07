package register

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

type HTTPClient interface {
	Do(req *fhttp.Request) (*fhttp.Response, error)
	SetCookies(u *url.URL, cookies []*fhttp.Cookie)
	GetCookies(u *url.URL) []*fhttp.Cookie
	SetFollowRedirect(followRedirect bool)
	CloseIdleConnections()
}

type HTTPClientFactory interface {
	New(cfg Config) (HTTPClient, error)
}

type defaultHTTPClientFactory struct{}

func (defaultHTTPClientFactory) New(cfg Config) (HTTPClient, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_133),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithTimeoutSeconds(int(cfg.TokenExchangeTimeout.Seconds())),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithInsecureSkipVerify(),
	}
	if strings.TrimSpace(cfg.ProxyURL) != "" {
		options = append(options, tlsclient.WithProxyUrl(strings.TrimSpace(cfg.ProxyURL)))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

type responseSnapshot struct {
	StatusCode int
	Header     map[string][]string
	Body       []byte
	URL        string
}

func (r responseSnapshot) JSON() map[string]any {
	var data map[string]any
	if err := json.Unmarshal(r.Body, &data); err != nil {
		return map[string]any{}
	}
	return data
}

func (r responseSnapshot) Text() string {
	return string(r.Body)
}

func doWithRetry(ctx context.Context, client HTTPClient, attempts int, reqFactory func() (*fhttp.Request, error)) (responseSnapshot, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, err := reqFactory()
		if err != nil {
			return responseSnapshot{}, err
		}
		req = req.WithContext(ctx)
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return responseSnapshot{}, readErr
			}
			snapshot := responseSnapshot{
				StatusCode: resp.StatusCode,
				Header:     cloneHeader(resp.Header),
				Body:       body,
			}
			if resp.Request != nil && resp.Request.URL != nil {
				snapshot.URL = resp.Request.URL.String()
			}
			return snapshot, nil
		}
		lastErr = err
		if i+1 < attempts {
			select {
			case <-ctx.Done():
				return responseSnapshot{}, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return responseSnapshot{}, lastErr
}

func newRequest(method string, rawURL string, headers map[string]string, body []byte) (*fhttp.Request, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := fhttp.NewRequest(strings.ToUpper(method), rawURL, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	return req, nil
}

func cloneHeader(src fhttp.Header) map[string][]string {
	out := make(map[string][]string, len(src))
	for key, values := range src {
		out[key] = append([]string(nil), values...)
	}
	return out
}

package push

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testSender(status int, body string, fail error) *Sender {
	// Minimal Sender assembled directly (same package sees private fields):
	// auth token pre-cached, RoundTripper fully stubbed, no network or signing.
	s := &Sender{config: Config{BundleID: "com.x0c.corral"}}
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if fail != nil {
			return nil, fail
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Apns-Id": []string{"apns-1"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	s.token = "stub"
	s.issuedAt = time.Now()
	return s
}

func TestSendEmptyTokenIsBadRequest(t *testing.T) {
	s := &Sender{config: Config{BundleID: "com.x0c.corral"}}
	if _, err := s.Send("", "production", "host", []byte("x")); !errors.Is(err, ErrBadPushRequest) {
		t.Fatalf("empty token must be bad_request, got %v", err)
	}
}

func TestSendAcceptedParsesApnsID(t *testing.T) {
	s := testSender(200, "", nil)
	result, err := s.Send("tok", "production", "host", []byte("x"))
	if err != nil {
		t.Fatalf("200 must succeed: %v", err)
	}
	if result.StatusCode != 200 || result.ApnsID != "apns-1" {
		t.Fatalf("wrong result: %+v", result)
	}
}

func TestSendBadDeviceTokenMaps(t *testing.T) {
	s := testSender(410, `{"reason":"Unregistered"}`, nil)
	result, err := s.Send("tok", "production", "host", []byte("x"))
	if !errors.Is(err, ErrBadDeviceToken) {
		t.Fatalf("410 Unregistered must be bad_token, got %v", err)
	}
	if result.Reason != "Unregistered" || result.StatusCode != 410 {
		t.Fatalf("reason/status must pass through: %+v", result)
	}
}

func TestSendThrottledMaps(t *testing.T) {
	s := testSender(429, `{"reason":"TooManyRequests"}`, nil)
	if _, err := s.Send("tok", "production", "host", []byte("x")); !errors.Is(err, ErrPushThrottled) {
		t.Fatalf("429 must be throttled, got %v", err)
	}
}

func TestSendNon200TreatedAsRejected(t *testing.T) {
	s := testSender(202, "", nil)
	if _, err := s.Send("tok", "production", "host", []byte("x")); !errors.Is(err, ErrPushRejected) {
		t.Fatalf("non-200 2xx must not count as accepted, got %v", err)
	}
}

func TestSendTransportOnNetworkError(t *testing.T) {
	s := testSender(0, "", errors.New("dial timeout"))
	if _, err := s.Send("tok", "production", "host", []byte("x")); !errors.Is(err, ErrPushTransport) {
		t.Fatalf("network error must be transport, got %v", err)
	}
}

func TestRouterWithoutSenderIsNotConfigured(t *testing.T) {
	router := NewRouter(nil)
	if _, err := router.Send("tok", "production", "host", "com.x0c.corral", []byte("x")); !errors.Is(err, ErrPushNotConfigured) {
		t.Fatalf("missing config must be no_push_config, got %v", err)
	}
}

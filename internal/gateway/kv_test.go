package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeKV is an injectable kvClient for the handler and failover tests.
type fakeKV struct {
	getVal   []byte
	getFound bool
	getErr   error
	putErr   error
	delErr   error
	puts     int
	deletes  int
}

func (f *fakeKV) Get(_ context.Context, _ []byte) ([]byte, bool, error) {
	return f.getVal, f.getFound, f.getErr
}
func (f *fakeKV) Put(_ context.Context, _, _ []byte) error { f.puts++; return f.putErr }
func (f *fakeKV) Delete(_ context.Context, _ []byte) error { f.deletes++; return f.delErr }
func (f *fakeKV) Close() error                             { return nil }

func srvWith(clients ...kvClient) *Server {
	s := &Server{kv: clients}
	s.mux = s.routes()
	return s
}

func TestDecodeValue(t *testing.T) {
	str := func(s string) *string { return &s }

	got, err := decodeValue(kvPutRequest{Value: str("hello")})
	if err != nil || string(got) != "hello" {
		t.Fatalf("value: %q %v", got, err)
	}
	got, err = decodeValue(kvPutRequest{ValueBase64: str("aGVsbG8=")}) // "hello"
	if err != nil || string(got) != "hello" {
		t.Fatalf("base64: %q %v", got, err)
	}
	if _, err := decodeValue(kvPutRequest{Value: str("a"), ValueBase64: str("Yg==")}); err == nil {
		t.Error("both value and value_base64 should error")
	}
	if _, err := decodeValue(kvPutRequest{}); err == nil {
		t.Error("neither value nor value_base64 should error")
	}
	if _, err := decodeValue(kvPutRequest{ValueBase64: str("not base64!!")}); err == nil {
		t.Error("bad base64 should error")
	}
}

func TestKVValueResponse(t *testing.T) {
	utf8Resp := kvValueResponse("k", []byte("shipped"))
	if utf8Resp.Value != "shipped" || utf8Resp.ValueBase64 == "" {
		t.Fatalf("utf8 value should populate both fields: %+v", utf8Resp)
	}
	binResp := kvValueResponse("k", []byte{0xff, 0xfe, 0x00})
	if binResp.Value != "" || binResp.ValueBase64 == "" {
		t.Fatalf("binary value should omit Value and set base64: %+v", binResp)
	}
}

func TestStatusToHTTP(t *testing.T) {
	cases := []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, http.StatusBadRequest},
		{codes.Unavailable, http.StatusServiceUnavailable},
		{codes.DeadlineExceeded, http.StatusGatewayTimeout},
		{codes.Internal, http.StatusBadGateway},
	}
	for _, c := range cases {
		got, _ := statusToHTTP(status.Error(c.code, "x"))
		if got != c.want {
			t.Errorf("code %v: got %d, want %d", c.code, got, c.want)
		}
	}
}

func TestKVGetHandler(t *testing.T) {
	srv := srvWith(&fakeKV{getVal: []byte("shipped"), getFound: true})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/kv/order:5005", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"value":"shipped"`) {
		t.Fatalf("expected value in body: %s", rr.Body.String())
	}

	// Not found -> 404.
	nf := srvWith(&fakeKV{getFound: false})
	rr = httptest.NewRecorder()
	nf.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/kv/missing", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing key should be 404, got %d", rr.Code)
	}

	// Node error (quorum) -> 502.
	er := srvWith(&fakeKV{getErr: status.Error(codes.Internal, "write quorum not met")})
	rr = httptest.NewRecorder()
	er.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/kv/x", nil))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("node error should be 502, got %d", rr.Code)
	}
}

func TestKVGetNoNodes(t *testing.T) {
	srv := srvWith()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/kv/x", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no nodes should be 503, got %d", rr.Code)
	}
}

func TestKVPutHandler(t *testing.T) {
	fake := &fakeKV{}
	srv := srvWith(fake)

	// Valid put.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/kv/order:5005", strings.NewReader(`{"value":"shipped"}`))
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || fake.puts != 1 {
		t.Fatalf("valid put: code %d puts %d", rr.Code, fake.puts)
	}

	// Invalid JSON -> 400.
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/v1/kv/k", strings.NewReader("not json")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid json should be 400, got %d", rr.Code)
	}

	// Missing value -> 400.
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/v1/kv/k", strings.NewReader(`{}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing value should be 400, got %d", rr.Code)
	}
}

func TestKVDeleteHandler(t *testing.T) {
	fake := &fakeKV{}
	srv := srvWith(fake)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/v1/kv/order:5005", nil))
	if rr.Code != http.StatusOK || fake.deletes != 1 {
		t.Fatalf("delete: code %d deletes %d", rr.Code, fake.deletes)
	}
}

func TestKVMutateFailover(t *testing.T) {
	// First node fails (unavailable), second succeeds: the mutation should succeed via failover.
	down := &fakeKV{putErr: status.Error(codes.Unavailable, "connection refused")}
	up := &fakeKV{}
	srv := &Server{kv: []kvClient{down, up}}
	if err := srv.kvMutate(context.Background(), func(c kvClient) error {
		return c.Put(context.Background(), []byte("k"), []byte("v"))
	}); err != nil {
		t.Fatalf("failover should succeed on the second node: %v", err)
	}
	if up.puts != 1 {
		t.Fatalf("second node should have been used, puts = %d", up.puts)
	}

	// All fail: returns the last error.
	all := &Server{kv: []kvClient{
		&fakeKV{putErr: status.Error(codes.Unavailable, "a")},
		&fakeKV{putErr: status.Error(codes.Internal, "b")},
	}}
	err := all.kvMutate(context.Background(), func(c kvClient) error {
		return c.Put(context.Background(), []byte("k"), []byte("v"))
	})
	if err == nil {
		t.Fatal("all-fail should return an error")
	}

	// No nodes: errNoNodes.
	empty := &Server{}
	if err := empty.kvMutate(context.Background(), func(c kvClient) error { return nil }); err != errNoNodes {
		t.Fatalf("no nodes should return errNoNodes, got %v", err)
	}
}

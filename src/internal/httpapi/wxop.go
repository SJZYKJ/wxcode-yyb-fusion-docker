package httpapi

import (
	"context"
	"net/http"
	"strings"

	"yyb_go/internal/eventlog"
)

type wxOperationKey struct{}

// withWXOperationContext tags a protocol call with the request class it asks
// for. Events and the code breaker use it to tell a code request from a phone
// or operateWxData one without changing invokeWXApp's signature.
func withWXOperationContext(ctx context.Context, op string) context.Context {
	return context.WithValue(ctx, wxOperationKey{}, op)
}

func withWXOperation(r *http.Request, op string) *http.Request {
	return r.WithContext(withWXOperationContext(r.Context(), op))
}

func wxOperation(ctx context.Context) string {
	op, _ := ctx.Value(wxOperationKey{}).(string)
	return op
}

// wxAPIName is the mini-program API an operateWxData payload names. The event
// log sanitizes it again before storing.
func wxAPIName(op string, payload map[string]any) string {
	if op != eventlog.OpOperateWXData {
		return ""
	}
	name, _ := payload["api_name"].(string)
	return strings.TrimSpace(name)
}

type refreshSourceKey struct{}

// withRefreshSource tags a credential refresh with what asked for it. The
// background loop leaves its context untagged and reports keepalive.* events
// itself; business recovery and manual refreshes are tagged so they can be
// counted separately in the event log.
func withRefreshSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, refreshSourceKey{}, source)
}

func refreshSource(ctx context.Context) string {
	source, _ := ctx.Value(refreshSourceKey{}).(string)
	return source
}

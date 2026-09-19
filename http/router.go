package http

import (
	"net/http"
	"strings"
)

type Router struct {
	routes []*Route
}

type Route struct {
	method      string
	path        string
	prefix      bool
	handler     appHandler
	matcherFunc func(*http.Request) bool
}

func NewRouter() *Router {
	return &Router{}
}

func (r *Router) Route(method, path string, handler appHandler) *Route {
	route := Route{
		method:  method,
		path:    path,
		handler: handler,
	}
	r.routes = append(r.routes, &route)
	return &route
}

func (r *Router) RoutePrefix(method, path string, handler appHandler) *Route {
	route := r.Route(method, path, handler)
	route.prefix = true
	return route
}

func (r *Router) Handler() http.Handler {
	return appHandler(func(w http.ResponseWriter, req *http.Request) *AppError {
		for _, route := range r.routes {
			if route.match(req) {
				return route.handler(w, req)
			}
		}
		return notFoundHandler(w, req)
	})
}

// Accept limits the route to requests whose Accept header asks for mediaType.
func (r *Route) Accept(mediaType string) *Route {
	return r.MatcherFunc(func(req *http.Request) bool {
		return acceptsMediaType(req, mediaType)
	})
}

// MatcherFunc limits the route to requests that f accepts.
func (r *Route) MatcherFunc(f func(*http.Request) bool) *Route {
	r.matcherFunc = f
	return r
}

func (r *Route) match(req *http.Request) bool {
	if req.Method != r.method {
		return false
	}
	if r.prefix {
		if !strings.HasPrefix(req.URL.Path, r.path) {
			return false
		}
	} else if r.path != req.URL.Path {
		return false
	}
	return r.matcherFunc == nil || r.matcherFunc(req)
}

package server

import (
	"net/http"
	"slices"
	"strings"
)

type router struct {
	routes []*route
}

type route struct {
	method  string
	path    string
	prefix  bool
	handler appHandler
	format  format
}

func newRouter() *router {
	return &router{}
}

func (r *router) Route(method, path string, handler appHandler) *route {
	rt := route{
		method:  method,
		path:    path,
		handler: handler,
	}
	r.routes = append(r.routes, &rt)
	return &rt
}

func (r *router) RoutePrefix(method, path string, handler appHandler) *route {
	route := r.Route(method, path, handler)
	route.prefix = true
	return route
}

// Handler dispatches to the first route that matches, and a failure takes the
// format of that route. A path that exists for other methods gets 405 with
// Allow in the format of the first route on it, or 204 with Allow for OPTIONS.
func (r *router) Handler() appHandler {
	return appHandler(func(w http.ResponseWriter, req *http.Request) *appError {
		var allowed []string
		var f format
		for _, route := range r.routes {
			if !route.serves(req) {
				continue
			}
			if route.matchMethod(req) {
				e := route.handler(w, req)
				if e != nil && e.format == negotiated {
					e.format = route.format
				}
				return e
			}
			if allowed == nil {
				f = route.format
			}
			allowed = appendMethods(allowed, route.method)
		}
		if len(allowed) == 0 {
			return newError(notFound, "No endpoint answers at this path.")
		}

		allow := strings.Join(append(allowed, http.MethodOptions), ", ")
		w.Header().Set("Allow", allow)
		if req.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		e := newError(methodNotAllowed, "This path answers "+allow+".")
		e.format = f
		return e
	})
}

// appendMethods adds method to allowed once, and HEAD after GET, because the
// router answers HEAD with the GET handler.
func appendMethods(allowed []string, method string) []string {
	allowed = appendOnce(allowed, method)
	if method == http.MethodGet {
		allowed = appendOnce(allowed, http.MethodHead)
	}
	return allowed
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// Format sets what the route answers in, and with it what its failures come
// in.
func (r *route) Format(f format) *route {
	r.format = f
	return r
}

// serves reports whether the route matches req apart from its method.
func (r *route) serves(req *http.Request) bool {
	if r.prefix {
		return strings.HasPrefix(req.URL.Path, r.path)
	}
	return r.path == req.URL.Path
}

// matchMethod reports whether the route answers the method of req. HEAD asks
// for the headers a GET would send, and net/http drops the body.
func (r *route) matchMethod(req *http.Request) bool {
	return req.Method == r.method || req.Method == http.MethodHead && r.method == http.MethodGet
}

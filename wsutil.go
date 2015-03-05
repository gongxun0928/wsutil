package wsutil

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ReverseProxy is a WebSocket reverse proxy. It will not work with a regular
// HTTP request, so it is the caller's responsiblity to ensure the incoming
// request is a WebSocket request.
type ReverseProxy struct {
	// Director must be a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	Director func(*http.Request)

	// Dial specifies the dial function for dialing the proxied
	// server over tcp.
	// If Dial is nil, net.Dial is used.
	Dial func(network, addr string) (net.Conn, error)

	// ErrorLog specifies an optional logger for errors
	// that occur when attempting to proxy the request.
	// If nil, logging goes to os.Stderr via the log package's
	// standard logger.
	ErrorLog *log.Logger
}

// stolen from net/http/httputil. singleJoiningSlash ensures that the route
// '/a/' joined with '/b' becomes '/a/b'.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// NewSingleHostReverseProxy returns a new websocket ReverseProxy. The path
// rewrites follow the same rules as the httputil.ReverseProxy. If the target
// url has the path '/foo' and the incoming request '/bar', the request path
// will be updated to '/foo/bar' before forwarding.
func NewSingleHostReverseProxy(target *url.URL) *ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
	}
	return &ReverseProxy{Director: director}
}

// Function to implement the http.Handler interface.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logFunc := log.Printf
	if p.ErrorLog != nil {
		logFunc = p.ErrorLog.Printf
	}
	outreq := new(http.Request)
	// shallow copying
	*outreq = *r
	p.Director(outreq)
	host := outreq.URL.Host
	// if host does not specify a port, default to port 80
	if !strings.Contains(host, ":") {
		host = host + ":80"
	}
	dial := p.Dial
	if dial == nil {
		dial = net.Dial
	}
	d, err := dial("tcp", host)
	if err != nil {
		http.Error(w, "Error forwarding request.", 500)
		logFunc("Error dialing websocket backend %s: %v", outreq.URL, err)
		return
	}
	// All request generated by the http package implement this interface.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Not a hijacker?", 500)
		return
	}
	// Hijack() tells the http package not to do anything else with the connection.
	// After, it bcomes this functions job to manage it. `nc` is of type *net.Conn.
	nc, _, err := hj.Hijack()
	if err != nil {
		logFunc("Hijack error: %v", err)
		return
	}
	defer nc.Close() // must close the underlying net connection after hijacking
	defer d.Close()

	err = outreq.Write(d) // write the modified incoming request to the dialed connection
	if err != nil {
		logFunc("Error copying request to target: %v", err)
		return
	}
	errc := make(chan error, 2)
	cp := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		errc <- err
	}
	go cp(d, nc)
	go cp(nc, d)
	<-errc
}

// IsWebSocketRequest returns a boolean indicating whether the request has the
// headers of a WebSocket handshake request.
func IsWebSocketRequest(r *http.Request) bool {
	if strings.ToLower(r.Header.Get("Connection")) != "upgrade" {
		return false
	}
	if strings.ToLower(r.Header.Get("Upgrade")) != "websocket" {
		return false
	}
	return true
}

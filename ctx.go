package apiserv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/missionMeteora/apiserv/router"
)

var (
	// ErrDir is Returned from ctx.File when the path is a directory not a file.
	ErrDir = errors.New("file is a directory")
)

// Context is the default context passed to handlers
// it is not thread safe and should never be used outside the handler
type Context struct {
	Params router.Params
	Req    *http.Request
	http.ResponseWriter

	data map[string]interface{}

	done bool

	status             int
	hijackServeContent bool
}

// Param is a shorthand for ctx.Params.Get(name).
func (ctx *Context) Param(key string) string {
	return ctx.Params.Get(key)
}

// Query is a shorthand for ctx.Req.URL.Query().Get(key).
func (ctx *Context) Query(key string) string {
	return ctx.Req.URL.Query().Get(key)
}

// Get returns a context value
func (ctx *Context) Get(key string) interface{} {
	return ctx.data[key]
}

// Set sets a context value, useful in passing data to other handlers down the chain
func (ctx *Context) Set(key string, val interface{}) {
	if ctx.data == nil {
		ctx.data = make(map[string]interface{})
	}

	ctx.data[key] = val
}

// Redirect is a helper function for http redirect.
// if perm is true the status code is http.StatusPermanentRedirect, otherwise http.StatusTemporaryRedirect.
func (ctx *Context) Redirect(url string, perm bool) {
	code := http.StatusTemporaryRedirect
	if perm {
		code = http.StatusPermanentRedirect
	}
	http.Redirect(ctx, ctx.Req, url, code)
}

// WriteReader outputs the data from the passed reader with optional content-type.
func (ctx *Context) WriteReader(contentType string, r io.Reader) (int64, error) {
	if contentType != "" {
		ctx.SetContentType(contentType)
	}
	return io.Copy(ctx, r)
}

// File serves a file using http.ServeContent.
// See http.ServeContent.
func (ctx *Context) File(fp string) error {
	f, err := os.Open(fp)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	if fi.IsDir() {
		return ErrDir
	}
	ctx.hijackServeContent = true
	http.ServeContent(ctx, ctx.Req, fp, fi.ModTime(), f)
	return nil
}

// Path is a shorthand for ctx.Req.URL.EscapedPath().
func (ctx *Context) Path() string {
	return ctx.Req.URL.EscapedPath()
}

// SetContentType sets the responses's content-type.
func (ctx *Context) SetContentType(typ string) {
	if typ == "" {
		return
	}
	h := ctx.Header()
	h.Set("Content-Type", typ)
	h.Set("X-Content-Type-Options", "nosniff") // fixes IE xss exploit
}

// ContentType returns the request's content-type.
func (ctx *Context) ContentType() string {
	return ctx.Req.Header.Get("Content-Type")
}

// Read is a QoL shorthand for ctx.Req.Body.Read.
// Context implements io.Reader
func (ctx *Context) Read(p []byte) (int, error) {
	return ctx.Req.Body.Read(p)
}

// CloseBody closes the request body.
func (ctx *Context) CloseBody() error {
	return ctx.Req.Body.Close()
}

// BindJSON parses the request's body as json, and closes the body.
// Note that unlike gin.Context.Bind, this does NOT verify the fields using special tags.
func (ctx *Context) BindJSON(out interface{}) error {
	err := json.NewDecoder(ctx).Decode(out)
	ctx.CloseBody()
	return err
}

// Printf is a QoL function to handle outputing plain strings with optional fmt.Printf-style formating.
// calling this function marks the Context as done, meaning any returned responses won't be written out.
func (ctx *Context) Printf(code int, contentType, s string, args ...interface{}) (int, error) {
	ctx.done = true

	if contentType == "" {
		contentType = MimePlain
	}

	ctx.SetContentType(contentType)

	if code > 0 {
		ctx.WriteHeader(code)
	}

	return fmt.Fprintf(ctx, s, args...)
}

// JSON outputs a json object, it is highly recommended to return *Response rather than use this directly.
// calling this function marks the Context as done, meaning any returned responses won't be written out.
func (ctx *Context) JSON(code int, indent bool, v interface{}) error {
	ctx.done = true
	ctx.SetContentType(MimeJSON)

	enc := json.NewEncoder(ctx)

	if indent {
		enc.SetIndent("", "\t")
	}

	if code > 0 {
		ctx.WriteHeader(code)
	}

	return enc.Encode(v)
}

// WriteHeader and Write are to implement ResponseWriter and allows ghetto hijacking of http.ServeContent errors,
// without them we'd end up with plain text errors, we wouldn't want that, would we?

// WriteHeader implements http.ResponseWriter
func (ctx *Context) WriteHeader(s int) {
	if ctx.status = s; ctx.hijackServeContent && ctx.status >= 300 {
		return
	}
	ctx.ResponseWriter.WriteHeader(s)

}

// WriteHeader implements http.ResponseWriter
func (ctx *Context) Write(p []byte) (int, error) {
	if ctx.hijackServeContent && ctx.status >= 300 {
		ctx.hijackServeContent = false
		NewErrorResponse(ctx.status, p).WriteToCtx(ctx)
		return len(p), nil
	}
	ctx.done = true
	return ctx.ResponseWriter.Write(p)
}

// Status returns last value written using WriteHeader.
func (ctx *Context) Status() int {
	return ctx.status
}

// Done returns wither the context is marked as done or not.
func (ctx *Context) Done() bool { return ctx.done }

// Break can be returned from a handler to break a handler chain.
// It doesn't write anything to the connection.
var Break = &Response{Code: -1}

// Handler is the default server Handler
// In a handler chain, returning a non-nil breaks the chain.
type Handler func(ctx *Context) *Response

type handlerChain []Handler

func (hh handlerChain) Serve(rw http.ResponseWriter, req *http.Request, p router.Params) {
	ctx := &Context{
		Params:         p,
		Req:            req,
		ResponseWriter: rw,
	}
L:
	for _, h := range hh {
		switch r := h(ctx); r {
		case nil: // do nothing on nil
		case Break: // break means break the chain
			break L
		default:
			if !ctx.done {
				r.WriteToCtx(ctx)
			}
			break L
		}
	}
}
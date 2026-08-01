package main

import (
	"context"
	"fmt"
	"strings"
)

// The open pages are exposed as resources as well as tools, so a client can
// show what the browser is looking at without spending a tool call on it.
//
// A page resource reads as its snapshot rather than its HTML: the snapshot is
// what a model can act on, and the HTML of a modern app is mostly noise.

const pageURIPrefix = "browser://"

func (s *Server) listResources(ctx context.Context) (any, *RPCError) {
	var resources []Resource
	if s.browser.Running() {
		for _, st := range s.browser.Pages() {
			title, _ := st.Page.Title()
			name := title
			if name == "" {
				name = st.Page.URL()
			}
			resources = append(resources, Resource{
				URI:         pageURIPrefix + st.ID,
				Name:        name,
				Title:       st.ID,
				Description: "Accessibility snapshot of " + st.Page.URL(),
				MimeType:    "text/plain",
			})
		}
	}
	return &ListResourcesResult{
		// Barely cacheable: a page changes under the client's feet, which is
		// the whole point of watching one.
		Result:    s.completeResult(ctx).cacheable(5000, CacheScopePrivate),
		Resources: resources,
	}, nil
}

func (s *Server) listResourceTemplates(ctx context.Context) any {
	return &ListResourceTemplatesResult{
		Result: s.completeResult(ctx).cacheable(3600000, CacheScopePrivate),
		ResourceTemplates: []ResourceTemplate{{
			URITemplate: pageURIPrefix + "{page_id}",
			Name:        "Page snapshot",
			Description: "The accessibility snapshot of one open page, by its id from browser_pages.",
			MimeType:    "text/plain",
		}},
	}
}

func (s *Server) readResource(ctx context.Context, req *Request) (any, *RPCError) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := req.Bind(&params); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(params.URI, pageURIPrefix) {
		return nil, Errorf(CodeInvalidParams, "unknown resource %q; page resources look like %spage-1", params.URI, pageURIPrefix)
	}
	st, err := s.browser.PageByID(strings.TrimPrefix(params.URI, pageURIPrefix))
	if err != nil {
		return nil, Errorf(CodeInvalidParams, "%v", err)
	}
	frames, err := takeSnapshot(st, snapshotOptions{})
	if err != nil {
		return nil, Errorf(CodeInternalError, "snapshotting %s: %v", st.ID, err)
	}
	return &ReadResourceResult{
		Result: s.completeResult(ctx).cacheable(5000, CacheScopePrivate),
		Contents: []ResourceContents{{
			URI:      params.URI,
			MimeType: "text/plain",
			Text:     fmt.Sprintf("%s\n\n%s", st.Page.URL(), renderSnapshot(frames)),
		}},
	}, nil
}

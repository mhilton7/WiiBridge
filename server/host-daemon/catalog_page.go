package main

import (
	"net/http"
	"strconv"
)

const catalogPageSize = 100

type catalogPagination struct {
	Label                              string
	Total, First, Last, Current, Pages int
	PreviousURL, NextURL               string
}

func catalogPage[T any](r *http.Request, key, label string, items []T) ([]T, catalogPagination) {
	page := catalogPagination{Label: label, Total: len(items), Pages: max(1, (len(items)+catalogPageSize-1)/catalogPageSize)}
	current, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || current < 1 {
		current = 1
	}
	page.Current = min(current, page.Pages)
	start := (page.Current - 1) * catalogPageSize
	end := min(start+catalogPageSize, len(items))
	if len(items) > 0 {
		page.First, page.Last = start+1, end
	}
	link := func(number int) string {
		query := r.URL.Query()
		query.Del("notice")
		query.Set(key, strconv.Itoa(number))
		return "?" + query.Encode() + "#catalog-viewer"
	}
	if page.Current > 1 {
		page.PreviousURL = link(page.Current - 1)
	}
	if page.Current < page.Pages {
		page.NextURL = link(page.Current + 1)
	}
	return items[start:end], page
}

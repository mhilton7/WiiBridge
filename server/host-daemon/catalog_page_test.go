package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"wiibridge/shared/model"
)

func TestCatalogPaginationBoundariesAndLinks(t *testing.T) {
	items := make([]int, 251)
	for index := range items {
		items[index] = index
	}
	for _, test := range []struct {
		query             string
		first, last, page int
	}{
		{"", 1, 100, 1}, {"-3", 1, 100, 1}, {"invalid", 1, 100, 1},
		{"2", 101, 200, 2}, {"999", 201, 251, 3},
		{"9999999999999999999999999", 1, 100, 1},
	} {
		request := httptest.NewRequest("GET", "/?wii_page="+test.query+"&gamecube_page=2&platform=wii&q=A%26B&notice=old", nil)
		rows, page := catalogPage(request, "wii_page", "Wii", items)
		if page.First != test.first || page.Last != test.last || page.Current != test.page ||
			len(rows) != test.last-test.first+1 || rows[0] != test.first-1 || page.Total != 251 {
			t.Fatalf("query=%q page=%+v rows=%d", test.query, page, len(rows))
		}
		for _, link := range []string{page.PreviousURL, page.NextURL} {
			if link == "" {
				continue
			}
			u, err := url.Parse(link)
			if err != nil || u.Query().Get("q") != "A&B" || u.Query().Get("platform") != "wii" ||
				u.Query().Get("gamecube_page") != "2" || u.Query().Has("notice") || u.Fragment != "catalog-viewer" {
				t.Fatalf("page link lost filters: %q", link)
			}
		}
	}
	rows, page := catalogPage(httptest.NewRequest("GET", "/?wii_page=9", nil), "wii_page", "Wii", []int(nil))
	if len(rows) != 0 || page.First != 0 || page.Last != 0 || page.Current != 1 {
		t.Fatalf("empty page=%+v", page)
	}
}

func TestDashboardPaginationKeepsEveryTitleSearchable(t *testing.T) {
	a := testApp(t)
	a.scan.Games = nil
	for index := 0; index < 205; index++ {
		a.scan.Games = append(a.scan.Games, model.Game{
			ID: fmt.Sprintf("T%05d", index), Title: fmt.Sprintf("Catalog title %03d", index),
		})
	}
	render := func(query string) string {
		response := httptest.NewRecorder()
		a.dashboard(response, httptest.NewRequest("GET", "/?platform=wii"+query, nil))
		return response.Body.String()
	}
	first := render("")
	if !strings.Contains(first, "Catalog title 000") || strings.Contains(first, "Catalog title 100") ||
		!strings.Contains(first, "100 of 205 matches") || !strings.Contains(first, "wii_page=2") {
		t.Fatal("first page was not bounded with a next link and total count")
	}
	last := render("&wii_page=3")
	if strings.Contains(last, "Catalog title 199") || !strings.Contains(last, "Catalog title 204") ||
		!strings.Contains(last, "Page 3 of 3") {
		t.Fatal("last page lost titles or boundaries")
	}
	search := render("&q=title+204")
	if !strings.Contains(search, "Catalog title 204") || strings.Contains(search, "Catalog title 000") {
		t.Fatal("search only considered the first page")
	}
}

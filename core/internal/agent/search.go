package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// SearchCostUSD is Brave's listed Search API price of $5.00 per 1,000 requests
// (checked 2026-10-31). Brave reports no per-request cost.
const SearchCostUSD = 0.005

const (
	braveSearchURL  = "https://api.search.brave.com/res/v1/web/search"
	maxSearchQuery  = 400
	searchResults   = 8
	searchTimeout   = 15 * time.Second
	searchBodyBytes = 2 << 20
)

var snippetTags = regexp.MustCompile(`<[^>]*>`)

// webSearch calls the provider directly so each search becomes an operation record
// that a completion check can cite. It is never repeated automatically.
func (s *Service) webSearch(ctx context.Context, query string) (map[string]any, error) {
	if s.Capabilities.SearchKey == "" {
		return map[string]any{"available": false, "reason": "Web search has no API key configured on this worker."}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	address := braveSearchURL + "?" + url.Values{"q": {query}, "count": {fmt.Sprint(searchResults)}, "text_decorations": {"false"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("web search request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", s.Capabilities.SearchKey)
	res, err := s.HTTP.Do(req)
	if err != nil {
		// A dropped response may already have been billed.
		return map[string]any{"cost_usd": SearchCostUSD}, errors.New("web search request failed before a response arrived")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web search returned HTTP %d", res.StatusCode)
	}
	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, searchBodyBytes)).Decode(&body) != nil {
		return map[string]any{"cost_usd": SearchCostUSD}, errors.New("web search returned an unreadable response")
	}
	results := []map[string]any{}
	for _, item := range body.Web.Results {
		result := map[string]any{"title": cleanSnippet(item.Title), "url": item.URL, "snippet": cleanSnippet(item.Description)}
		if item.PageAge != "" {
			result["published"] = item.PageAge
		} else if item.Age != "" {
			result["published"] = item.Age
		}
		results = append(results, result)
	}
	return map[string]any{"query": query, "retrieved_at": s.now().Format(time.RFC3339), "results": results, "cost_usd": SearchCostUSD}, nil
}

func cleanSnippet(text string) string {
	return html.UnescapeString(snippetTags.ReplaceAllString(text, ""))
}

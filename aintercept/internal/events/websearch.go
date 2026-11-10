package events

import (
	"encoding/json"
	"sort"
	"strings"
)

// WebSearch summarizes a web-search call: how many queries ran, how many
// results came back, and which sites they came from. Queries, snippets and
// page content are not kept.
type WebSearch struct {
	Queries           int      `json:"queries"`
	Results           int      `json:"results"`
	ResultDomains     []string `json:"result_domains,omitempty"` // distinct, sorted
	ExternalWebAccess *bool    `json:"external_web_access,omitempty"`
}

// maxResultDomains bounds ResultDomains.
const maxResultDomains = 100

// WebSearchFromBodies parses Codex's web-search service
// (POST /backend-api/codex/alpha/search): request commands.search_query[],
// settings.external_web_access; response results[].domain. nil otherwise.
func WebSearchFromBodies(path string, req, resp []byte, respEncoding string) *WebSearch {
	if !strings.HasSuffix(strings.SplitN(path, "?", 2)[0], "/alpha/search") {
		return nil
	}
	var rq struct {
		Commands struct {
			SearchQuery []json.RawMessage `json:"search_query"`
		} `json:"commands"`
		Settings struct {
			ExternalWebAccess *bool `json:"external_web_access"`
		} `json:"settings"`
	}
	_ = json.Unmarshal(req, &rq)
	ws := &WebSearch{Queries: len(rq.Commands.SearchQuery), ExternalWebAccess: rq.Settings.ExternalWebAccess}
	if raw, err := decodeBody(resp, respEncoding); err == nil {
		var rs struct {
			Results []struct {
				Domain string `json:"domain"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &rs) == nil {
			ws.Results = len(rs.Results)
			seen := map[string]bool{}
			for _, r := range rs.Results {
				if d := strings.ToLower(strings.TrimSpace(r.Domain)); d != "" && !seen[d] && len(seen) < maxResultDomains {
					seen[d] = true
					ws.ResultDomains = append(ws.ResultDomains, d)
				}
			}
			sort.Strings(ws.ResultDomains)
		}
	}
	return ws
}

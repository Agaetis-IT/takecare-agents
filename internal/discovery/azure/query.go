// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// APIVersion is the Resource Graph REST API version whose QueryRequest and
// QueryResponse shapes this package writes and reads.
const APIVersion = "2024-04-01"

const (
	// pageSize is $top, the documented maximum.
	pageSize = 1000
	// maxPages bounds one query's paging: 50,000 rows.
	maxPages = 50
	// maxSubscriptions is the most whole subscriptions one request carries.
	maxSubscriptions = 100
)

// Request is one Resource Graph query: a fixed query over explicit
// subscriptions, never a management group nor the tenant, and the
// continuation token of the page to read.
type Request struct {
	Query         string
	Subscriptions []string
	SkipToken     string
}

// Body is the request's QueryRequest, as POSTed to
// /providers/Microsoft.ResourceGraph/resources?api-version=2024-04-01.
func (r Request) Body() ([]byte, error) {
	type options struct {
		Top          int    `json:"$top"`
		SkipToken    string `json:"$skipToken,omitempty"`
		ResultFormat string `json:"resultFormat"`
	}
	return json.Marshal(struct {
		Subscriptions []string `json:"subscriptions"`
		Query         string   `json:"query"`
		Options       options  `json:"options"`
	}{r.Subscriptions, r.Query, options{pageSize, r.SkipToken, "objectArray"}})
}

// Querier runs one request and returns the QueryResponse's raw body. The
// transport (credential, throttling headers, retries within the import's
// deadline) is the caller's.
type Querier interface {
	Query(ctx context.Context, req Request) ([]byte, error)
}

// The fixed queries. Only the columns an entry needs are projected: never
// properties nor tags, which carry settings.
const (
	queryGroups = "ResourceContainers | where type =~ 'microsoft.resources/subscriptions/resourcegroups'%s" +
		" | project id, name, subscriptionId, location | order by id asc"
	queryResources = "Resources | where type in~ (%s)%s" +
		" | project id, name, type, subscriptionId, resourceGroup, location | order by id asc"
)

// groupName is the API's ResourceGroupName pattern: a name that matches
// it holds no quote, so it can be written in a query.
var groupName = regexp.MustCompile(`^[-_.()\p{Lu}\p{Ll}\p{Lt}\p{Lm}\p{Lo}\p{Nd}]{0,89}[-_()\p{Lu}\p{Ll}\p{Lt}\p{Lm}\p{Lo}\p{Nd}]$`)

func quoted(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "'" + v + "'"
	}
	return strings.Join(q, ", ")
}

// groupsQuery lists resource groups, only the named ones when given.
func groupsQuery(groups []string) string {
	if len(groups) == 0 {
		return fmt.Sprintf(queryGroups, "")
	}
	return fmt.Sprintf(queryGroups, " | where name in~ ("+quoted(groups)+")")
}

// resourcesQuery lists the resources of the mapped types, only in the
// named groups when given.
func resourcesQuery(groups []string) string {
	where := ""
	if len(groups) > 0 {
		where = " | where resourceGroup in~ (" + quoted(groups) + ")"
	}
	return fmt.Sprintf(queryResources, quoted(mappedTypes()), where)
}

// row is what the importer keeps of a returned row.
type row struct {
	ID, Name, Type, Subscription, ResourceGroup string
}

var errUnrecognized = errors.New("unrecognized Resource Graph answer")

// page is a parsed QueryResponse.
type page struct {
	rows      []row
	skipToken string
	truncated bool
}

// parsePage reads a QueryResponse strictly: count, data as an object
// array, resultTruncated as the documented "true" or "false", and each
// row's id, name and subscriptionId as strings; type and resourceGroup
// when the query projected them.
func parsePage(raw []byte, resources bool) (page, error) {
	var resp struct {
		Count           *int64            `json:"count"`
		Data            []json.RawMessage `json:"data"`
		ResultTruncated *string           `json:"resultTruncated"`
		SkipToken       string            `json:"$skipToken"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&resp); err != nil || resp.Count == nil || resp.Data == nil || resp.ResultTruncated == nil {
		return page{}, errUnrecognized
	}
	if *resp.ResultTruncated != "true" && *resp.ResultTruncated != "false" || *resp.Count != int64(len(resp.Data)) {
		return page{}, errUnrecognized
	}
	p := page{skipToken: resp.SkipToken, truncated: *resp.ResultTruncated == "true"}
	for _, d := range resp.Data {
		var r struct {
			ID             *string `json:"id"`
			Name           *string `json:"name"`
			Type           *string `json:"type"`
			SubscriptionID *string `json:"subscriptionId"`
			ResourceGroup  *string `json:"resourceGroup"`
		}
		if err := json.Unmarshal(d, &r); err != nil || r.ID == nil || r.Name == nil || r.SubscriptionID == nil {
			return page{}, errUnrecognized
		}
		got := row{ID: *r.ID, Name: *r.Name, Subscription: strings.ToLower(*r.SubscriptionID)}
		if resources {
			if r.Type == nil || r.ResourceGroup == nil {
				return page{}, errUnrecognized
			}
			got.Type, got.ResourceGroup = strings.ToLower(*r.Type), strings.ToLower(*r.ResourceGroup)
		}
		p.rows = append(p.rows, got)
	}
	return p, nil
}

// Reasons of a notSeen line: fixed phrases, never the service's text.
const (
	reasonCallFailed   = "the Resource Graph query failed"
	reasonUnrecognized = "unrecognized Resource Graph answer"
	reasonTruncated    = "the answer was truncated without a continuation token: the rows after it were not read"
	reasonPageBound    = "the query returned more than 50 pages (50,000 rows): the rows after them were not read"
	reasonOutside      = "%d rows outside the connector's reach or the mapped types were dropped"
	reasonGroupName    = "the resource group name does not follow Azure's naming rule: not queried"
	reasonKeyTaken     = "another entry holds this key: not proposed"
	reasonInvalidEntry = "the import's proposal for this entry does not pass the Map's schema: not proposed"
)

// readAll runs a request and follows its $skipToken with the same query
// and scopes, at most maxPages pages. It returns the rows read and, when
// not every row was read, the reason; an error only when ctx ended.
func readAll(ctx context.Context, q Querier, req Request, resources bool) ([]row, string, error) {
	var rows []row
	for range maxPages {
		raw, err := q.Query(ctx, req)
		if ctx.Err() != nil {
			return nil, "", fmt.Errorf("resource graph query: %w", ctx.Err())
		}
		if err != nil {
			return rows, reasonCallFailed, nil
		}
		p, err := parsePage(raw, resources)
		if err != nil {
			return rows, reasonUnrecognized, nil
		}
		rows = append(rows, p.rows...)
		switch {
		case p.skipToken != "":
			req.SkipToken = p.skipToken
		case p.truncated:
			return rows, reasonTruncated, nil
		default:
			return rows, "", nil
		}
	}
	return rows, reasonPageBound, nil
}

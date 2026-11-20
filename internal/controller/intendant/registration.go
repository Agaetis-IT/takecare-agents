// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// maxRegistrationSet bounds the settled registrations a survey's spec
// keeps (spec 10 §3): past it, the set is not stored.
const maxRegistrationSet = 200

// Each entry of a registration set reads "<Kind> <ref> <generation>": the
// ref is the name, with its namespace when that is not the Intendant's;
// the generation is a number, or "absent" for a bound connector that does
// not exist.
func splitEntry(e string) (key, gen string) {
	i := strings.LastIndexByte(e, ' ')
	if i < 0 {
		return e, ""
	}
	return e[:i], e[i+1:]
}

// storedSet is the set a survey's spec keeps: nil past the bound.
func storedSet(set []string) []string {
	if len(set) > maxRegistrationSet {
		return nil
	}
	return set
}

// registrationReason says what changed between the newest survey's set
// (old; oldKnown false when that survey kept none: made before this
// field, or past the bound) and the current one: removed, then added,
// then changed, each sorted, cut at the reason's bound with how many do
// not fit.
func registrationReason(old []string, oldKnown bool, cur []string) string {
	const lead = "the registrations changed"
	if !oldKnown {
		if len(cur) > maxRegistrationSet {
			return lead + "; the set is too large to list"
		}
		return lead
	}
	was := map[string]string{}
	for _, e := range old {
		k, g := splitEntry(e)
		was[k] = g
	}
	is := map[string]string{}
	for _, e := range cur {
		k, g := splitEntry(e)
		is[k] = g
	}
	var removed, added, changed []string
	for k := range was {
		if _, ok := is[k]; !ok {
			removed = append(removed, entryWords(k)+" removed")
		}
	}
	for k, g := range is {
		before, ok := was[k]
		switch {
		case !ok:
			added = append(added, entryWords(k)+" added")
		case before != g && g == "absent":
			changed = append(changed, entryWords(k)+" changed (absent)")
		case before != g:
			changed = append(changed, entryWords(k)+" changed (generation "+g+")")
		}
	}
	slices.Sort(removed)
	slices.Sort(added)
	slices.Sort(changed)
	items := slices.Concat(removed, added, changed)
	if len(cur) > maxRegistrationSet {
		return fmt.Sprintf("%d registrations changed; the set is too large to list", len(items))
	}
	if len(items) == 0 {
		return lead
	}
	out := lead + ": " + items[0]
	if len(out) > maxReason {
		return out[:maxReason]
	}
	for i, it := range items[1:] {
		rest := len(items) - 1 - i
		more := fmt.Sprintf("; and %d more", rest)
		if len(out)+len("; ")+len(it)+len(more) > maxReason && (rest != 1 || len(out)+len("; ")+len(it) > maxReason) {
			return out + more
		}
		out += "; " + it
	}
	return out
}

// entryWords is an entry's key as a reader reads it: the kind in lower
// case, then the ref ("agent kagent/takecare-doc-east").
func entryWords(key string) string {
	kind, ref, _ := strings.Cut(key, " ")
	return strings.ToLower(kind) + " " + ref
}

// emptyDigest is the registrations digest of an empty set: no settled
// registration at all.
var emptyDigest = func() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}()

// baselineKnown says whether a survey's spec holds the set its digest was
// made from. An empty set is not stored (omitempty), so it reads as absent;
// it is known when the survey's digest is the empty set's. A survey made
// before this field existed, or one whose set was too large, is not known.
func baselineKnown(last v1alpha1.TakeCareSurveySpec) bool {
	return len(last.RegistrationSet) > 0 || last.Registrations == emptyDigest
}

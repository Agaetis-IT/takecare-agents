// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

// The phone number mask (spec 09 §4): patterns of our own, rendered in
// place of agentgateway's PhoneNumber builtin, which takes ISO dates,
// timestamps' dates and ids such as INC-2024-0312 for phone numbers and
// misses national numbers (measured on 1.6.0 and 1.7.0, 2026-11-18).
//
// They run on the gateway's Rust regex engine: no lookaround and no
// backreference. Each pattern is anchored on what a phone number starts
// with (+, 00 or a national 0), bounded by \b, and keeps its separators
// uniform by listing one alternative per separator instead of a
// backreference. A match is replaced by <masked>: at 1.6.0 a regex entry
// cannot name its replacement.
const (
	// PhoneE164 takes a number with its + and country code (1 to 3
	// digits, the first 1 to 9), an optional (0), then a national part
	// of 9 to 11 digits whose separators are all spaces, all dots or all
	// dashes, or none: +33 1 23 45 67 89, +33(0)1 23 45 67 89, +44 20 7946
	// 0958, +1 415-555-0123, +4930123456. The + keeps dates, ids and
	// quantities out; the first digit 1 to 9 keeps out time zone offsets
	// (+0100, +01:00) and exponents (1.7e+09); the uniform separators and
	// the 9-digit floor keep out a date after a sign (UTC+1 2026-11-18:
	// 8 digits, and the hour after it is joined by another separator);
	// the 11-digit ceiling keeps a year that follows a number out of it.
	PhoneE164 = `\+[1-9][0-9]{0,2}(?:[ .-]?\(0\))?[ .-]?(?:[0-9](?: ?[0-9]){8,10}|[0-9](?:\.?[0-9]){8,10}|[0-9](?:-?[0-9]){8,10})\b`

	// PhoneInternational00 takes the same with 00 in place of +, 11 to 14
	// digits after the 00 (a year that follows stays out), separated by nothing, spaces or dots: 0033 1 23
	// 45 67 89, 0044 20 7946 0958, 001 415 555 0123. No dash, so that an
	// all-digit UID prefix cannot form one; at least 11 digits, so that a
	// 12-digit AWS account id starting 00 is not one. It starts at the
	// start of a line or after a character that is neither a letter, a
	// digit, an underscore nor a point, and that character is masked with
	// the number: without lookbehind, this keeps a decimal fraction out
	// (0.00123456789012).
	PhoneInternational00 = `(?:(?m:^)|[^0-9A-Za-z_.])00[1-9](?:[ .]?[0-9]){10,13}\b`

	// PhoneFrench takes a French national number: 0 then 1 to 9, then four
	// pairs separated by the same space, dot or dash: 01 23 45 67 89,
	// 06.12.34.56.78, 01-23-45-67-89; or by nothing, 0123456789, starting
	// as PhoneInternational00 does, so that a decimal fraction
	// (0.0123456789) is not one. Uniform separators keep a European date
	// (05.11.2026) out; 00 is not one.
	PhoneFrench = `\b0[1-9](?: [0-9]{2}){4}\b|\b0[1-9](?:\.[0-9]{2}){4}\b|\b0[1-9](?:-[0-9]{2}){4}\b|(?:(?m:^)|[^0-9A-Za-z_.])0[1-9][0-9]{8}\b`

	// PhoneNorthAmerican takes a North American national number written
	// with separators: (415) 555-0123, 415 555-0123, 415-555-0123,
	// 415.555.0123. The area code and the exchange start with 2 to 9; the
	// 3-3-4 grouping does not collide with dates (4-2-2) or ids.
	PhoneNorthAmerican = `\(?\b[2-9][0-9]{2}\)? [2-9][0-9]{2}-[0-9]{4}\b|\b[2-9][0-9]{2}-[2-9][0-9]{2}-[0-9]{4}\b|\b[2-9][0-9]{2}\.[2-9][0-9]{2}\.[0-9]{4}\b`
)

// PhonePatterns are the patterns the masking Marker renders for the
// PhoneNumber builtin, in this order.
var PhonePatterns = []string{PhoneE164, PhoneInternational00, PhoneFrench, PhoneNorthAmerican}

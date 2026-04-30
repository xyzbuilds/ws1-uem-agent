package main

import (
	"strings"
)

// Region is one entry in the canonical OAuth token URL table for
// Omnissa Workspace ONE UEM SaaS. Source of truth: the
// "Datacenter and Token URLs for OAuth 2.0 Support" section of
// https://docs.omnissa.com/bundle/WorkspaceONE-UEM-Console-BasicsVSaaS/page/UsingUEMFunctionalityWithRESTAPI.html
//
// The Code field is what users pass to `ws1 profile add --region`.
// DataCenter is the SaaS data center hosting the tenant; Customers is
// the list of customer geos served by that data center; TokenURL is
// the OAuth 2.0 token endpoint.
type Region struct {
	Code       string
	DataCenter string
	Customers  []string
	TokenURL   string
}

// Regions is the canonical list. Update this when Omnissa adds or
// renames a data center; the test suite spot-checks it for stability.
//
// Note: an older URL form on `*.uemauth.vmwservices.com` is being
// deprecated in favor of the workspaceone.com domain shown here. If a
// user has a profile with the legacy URL it continues to work — the
// CLI stores AuthURL verbatim and only consults this table when the
// user passes --region.
var Regions = []Region{
	{
		Code:       "uat",
		DataCenter: "Ohio (United States)",
		Customers:  []string{"All UAT environment"},
		TokenURL:   "https://uat.uemauth.workspaceone.com/connect/token",
	},
	{
		Code:       "na",
		DataCenter: "Virginia (United States)",
		Customers:  []string{"United States", "Canada"},
		TokenURL:   "https://na.uemauth.workspaceone.com/connect/token",
	},
	{
		Code:       "emea",
		DataCenter: "Frankfurt (Germany)",
		Customers:  []string{"United Kingdom", "Germany"},
		TokenURL:   "https://emea.uemauth.workspaceone.com/connect/token",
	},
	{
		Code:       "apac",
		DataCenter: "Tokyo (Japan)",
		Customers:  []string{"India", "Japan", "Singapore", "Australia", "Hong Kong"},
		TokenURL:   "https://apac.uemauth.workspaceone.com/connect/token",
	},
}

// regionToAuthURL maps a region code to the canonical token URL.
// Returns ok=false for unknown codes; the caller surfaces a clear
// error listing valid regions (typically by referencing
// `ws1 profile regions`).
func regionToAuthURL(code string) (string, bool) {
	for _, r := range Regions {
		if r.Code == code {
			return r.TokenURL, true
		}
	}
	return "", false
}

// regionCodes returns the list of valid --region values for help and
// error messages. Order matches the Regions slice (uat first, then
// na/emea/apac in geographic order).
func regionCodes() []string {
	out := make([]string, 0, len(Regions))
	for _, r := range Regions {
		out = append(out, r.Code)
	}
	return out
}

// regionCodesString is regionCodes joined with " | " for help-text
// formatting (e.g. "uat | na | emea | apac").
func regionCodesString() string { return strings.Join(regionCodes(), " | ") }

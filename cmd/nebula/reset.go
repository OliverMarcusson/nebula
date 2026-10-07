package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
	"github.com/olivermarcusson/nebula/internal/resets"
	"github.com/olivermarcusson/nebula/internal/sessions"
)

// Usage resets are redeemed here, on a device holding the account, once the
// server has a passkey approval for that exact request. Claude's endpoint is
// the one Claude Code's own reset prompt calls. A redemption is sent at most
// once; anything ambiguous is reported as unknown, never retried.

const resetURL = "https://api.anthropic.com/api/organizations/%s/reset_rate_limits"

var resetMessages = map[string]string{
	"already_used": "Claude says this reset was already used",
	"not_limited":  "Claude only allows this reset while the account is at a usage limit",
	"cooldown":     "Claude asks to wait before using another reset",
	"ineligible":   "Claude says this account cannot use the reset",
	"unavailable":  "Claude says the reset is unavailable",
}

// claimResets redeems the approved requests the server hands this device.
func (a *loginAgent) claimResets(ctx context.Context) {
	var list []resets.Reset
	if err := a.c.call(ctx, "POST", "/v1/devices/"+a.device+"/resets", nil, &list); err != nil {
		return
	}
	for _, r := range list {
		state, msg := redeem(ctx, a.profiles, r)
		log.Printf("Reset %s for account %s: %s %s", r.ID, r.AccountID, state, msg)
		body := map[string]string{"state": state, "message": msg}
		for try := 0; try < 3; try++ {
			var out resets.Reset
			if err := a.c.call(context.Background(), "PUT", "/v1/devices/"+a.device+"/resets/"+r.ID, body, &out); err == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
}

func redeem(ctx context.Context, list string, r resets.Reset) (state, msg string) {
	if !sessions.ID(r.OrganizationID) || !accounts.GrantID.MatchString(r.GrantID) {
		return resets.Failed, "Invalid request"
	}
	profiles, err := claudeProfiles(list)
	if err != nil {
		return resets.Failed, "Could not read this device's Claude profiles"
	}
	var file string
	for _, p := range profiles {
		q, err := accounts.ReadLocal(p.Name, p.ConfigFile)
		if err == nil && q != nil && q.AccountUUID == r.AccountID && q.OrganizationUUID == r.OrganizationID {
			if _, err := accessToken(credentialsFile(p)); err == nil {
				file = credentialsFile(p)
				usageCache.Lock()
				delete(usageCache.at, p.Dir) // report the result on the next sync
				usageCache.Unlock()
				break
			}
		}
	}
	if file == "" {
		return resets.Failed, "This device has no valid sign-in for the account"
	}
	// The approval names one grant; redeem only if Claude still offers it.
	u, err := fetchUsage(ctx, file)
	if err != nil {
		return resets.Failed, "Could not check the account's resets with Claude"
	}
	offered := false
	for _, g := range u.Grants {
		offered = offered || (g.ID == r.GrantID && g.ResetsLeft > 0)
	}
	if !offered {
		return resets.Failed, "Claude no longer offers this reset"
	}
	payload, _ := json.Marshal(map[string]string{"program": "cedar_ember", "grant_id": r.GrantID, "request_id": r.ID})
	rctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	body, status, err := anthropic(rctx, file, "POST", fmt.Sprintf(resetURL, r.OrganizationID), bytes.NewReader(payload))
	switch {
	case err != nil || status >= 500:
		return resets.Unknown, "No clear answer from Claude; check the account's usage before trying again"
	case status == http.StatusTooManyRequests:
		return resets.Failed, "Claude is rate limiting reset requests; try again later"
	case status != http.StatusOK:
		return resets.Failed, fmt.Sprintf("Claude refused the reset (HTTP %d)", status)
	}
	var res struct {
		Result     string `json:"result"`
		Reason     string `json:"reason"`
		ResetsLeft *int   `json:"resets_left"`
	}
	if json.Unmarshal(body, &res) != nil || res.Result == "" {
		return resets.Unknown, "Claude's answer could not be read; check the account's usage"
	}
	if res.Result == "reset" {
		if res.ResetsLeft != nil {
			return resets.Succeeded, fmt.Sprintf("%d left on this grant", *res.ResetsLeft)
		}
		return resets.Succeeded, ""
	}
	if m, ok := resetMessages[res.Result]; ok {
		return resets.Failed, m
	}
	return resets.Failed, "Claude did not reset: " + res.Result
}

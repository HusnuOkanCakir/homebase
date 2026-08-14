package hostclient

import "context"

// Putting a broken server back together.
//
// Three operations, in the order somebody needs them: collect what is wrong,
// try to fix it, and — last — start again without losing the photographs.

// Diagnostics is where the bundle went, and what is in it.
type Diagnostics struct {
	Path      string   `json:"path"`
	Bytes     int64    `json:"bytes"`
	CreatedAt string   `json:"created_at"`
	Includes  []string `json:"includes"`

	// Excludes is what somebody checks before sending the file to a stranger.
	// It travels with the result rather than living in the documentation,
	// because the question is asked at the moment of sending.
	Excludes []string `json:"excludes"`

	Message string `json:"message"`
}

// RepairStep is one thing that was checked, and what was done about it.
type RepairStep struct {
	What    string `json:"what"`
	Done    string `json:"done,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// RepairResult is what repair looked at and what it changed.
type RepairResult struct {
	Steps []RepairStep `json:"steps"`

	// Changed being zero is the answer that matters most: it means whatever is
	// wrong is not something repair knows how to fix.
	Changed int    `json:"changed"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

// FactoryResetResult is what was removed and what was left.
type FactoryResetResult struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Message string   `json:"message"`
}

func (c *Client) CollectDiagnostics(ctx context.Context) (*Diagnostics, error) {
	var result Diagnostics
	if err := c.Call(ctx, "system.diagnostics", struct{}{}, false, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) Repair(ctx context.Context) (*RepairResult, error) {
	var result RepairResult
	if err := c.Call(ctx, "system.repair", struct{}{}, false, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// FactoryReset removes every account and every setting. confirm must be the
// server's hostname; hostd checks it again.
//
// keepData is passed explicitly rather than as a pointer with a default,
// because a caller in this package should have had to decide.
func (c *Client) FactoryReset(ctx context.Context, confirm string, keepData bool) (*FactoryResetResult, error) {
	params := struct {
		Confirm  string `json:"confirm"`
		KeepData bool   `json:"keep_data"`
	}{Confirm: confirm, KeepData: keepData}

	var result FactoryResetResult
	if err := c.Call(ctx, "system.factory_reset", params, true, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

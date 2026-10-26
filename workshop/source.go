package workshop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/catalystcommunity/magi/execution"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/workflow"
	"io"
	"regexp"
	"unicode/utf8"
)

const SourceBundleVersion = "opal-offline-source-v1"
const SourcePath = "harness/formatting.py"

// SourceBundle is review material, not a signed approval or a build result.
// The caller must read Record through the authorized owner-scoped store first.
// Only the installed demo module is eligible. No caller-selected paths or
// executable build instructions enter this object.
type SourceBundle struct {
	Version         string      `json:"version"`
	Owner           w.ScopeRef  `json:"owner"`
	Evidence        w.RecordRef `json:"evidence"`
	Path            string      `json:"path"`
	Base            string      `json:"base"`
	BaseDigest      string      `json:"base_digest"`
	Candidate       string      `json:"candidate"`
	CandidateDigest string      `json:"candidate_digest"`
	Checks          []Event     `json:"checks"`
	ChecksDigest    string      `json:"checks_digest"`
}

func sourceDigest(v []byte) string {
	h := sha256.Sum256(v)
	return hex.EncodeToString(h[:])
}

func sourceText(v string) bool { return len(v) > 0 && len(v) <= 16384 && utf8.ValidString(v) }

func strictJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return workflow.ErrInvalid
	}
	return nil
}

// PrepareSource can export after environment cleanup. It checks the retained
// final check and export, not just a mutable run state or model success claim.
func PrepareSource(owner w.ScopeRef, authority w.IdentityRef, record Record) (SourceBundle, error) {
	var empty SourceBundle
	if (record.State != "candidate_ready" && record.State != "closed") || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`).MatchString(record.Run) || !sourceText(record.Candidate) || sourceDigest([]byte(record.Candidate)) != record.Digest || !workflow.ValidateDefinition(Definition(owner)).Valid || authority.Domain == "" || authority.AccountId == "" {
		return empty, workflow.ErrInvalid
	}
	selected := make([]Event, 0, 3)
	base := ""
	for _, operation := range []string{"seed", "final-check", "export"} {
		var matches []Event
		for _, event := range record.Events {
			if event.Operation == operation {
				matches = append(matches, event)
			}
		}
		if len(matches) != 1 {
			return empty, workflow.ErrInvalid
		}
		event := matches[0]
		if event.Kind != "tool" || event.Outcome != "returned" || event.StartedAt.IsZero() || event.FinishedAt == nil || event.FinishedAt.Before(event.StartedAt) || len(selected) > 0 && event.StartedAt.Before(*selected[len(selected)-1].FinishedAt) {
			return empty, workflow.ErrInvalid
		}
		var request struct {
			Proposal struct {
				Tool      string            `json:"tool"`
				Arguments map[string]string `json:"arguments"`
			} `json:"proposal"`
			Decision    string `json:"decision"`
			Policy      string `json:"policy"`
			Environment string `json:"environment"`
			Image       string `json:"image"`
		}
		var result struct {
			Outcome       string `json:"outcome"`
			Text          string `json:"text"`
			ExitCode      *int   `json:"exit_code"`
			CheckedDigest string `json:"checked_digest,omitempty"`
		}
		if strictJSON(event.Request, &request) != nil || strictJSON(event.Response, &result) != nil || request.Decision != "allowed" || request.Environment == "" || request.Environment != record.EnvironmentID || request.Policy != execution.PolicyRevision || request.Image != execution.PythonImage || result.Outcome != "returned" || result.ExitCode == nil || *result.ExitCode != 0 {
			return empty, workflow.ErrInvalid
		}
		// Preserve the installed policy and image in the receipts. A later build
		// must admit its own recipe; these historic labels do not authorize it.
		args := request.Proposal.Arguments
		switch operation {
		case "seed":
			base = args["text"]
			if request.Proposal.Tool != "write_file" || len(args) != 2 || args["path"] != "solution.py" || !sourceText(base) {
				return empty, workflow.ErrInvalid
			}
		case "final-check":
			if request.Proposal.Tool != "check" || len(args) != 0 || result.CheckedDigest != record.Digest {
				return empty, workflow.ErrInvalid
			}
		case "export":
			if request.Proposal.Tool != "read_file" || len(args) != 1 || args["path"] != "solution.py" || result.Text != record.Candidate {
				return empty, workflow.ErrInvalid
			}
		}
		selected = append(selected, event)
	}
	if base == record.Candidate {
		return empty, workflow.ErrInvalid
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return empty, err
	}
	return SourceBundle{Version: SourceBundleVersion, Owner: owner, Evidence: w.RecordRef{Authority: authority, Kind: "development-evidence", RecordId: record.Run}, Path: SourcePath, Base: base, BaseDigest: sourceDigest([]byte(base)), Candidate: record.Candidate, CandidateDigest: record.Digest, Checks: selected, ChecksDigest: sourceDigest(raw)}, nil
}

// ValidateSource checks integrity only. An untrusted caller can create matching
// hashes. Obtain bundles from an admitted source and keep release policy separate.
func ValidateSource(bundle SourceBundle, current []byte) error {
	if bundle.Version != SourceBundleVersion || bundle.Path != SourcePath || bundle.Evidence.Kind != "development-evidence" || len(bundle.Checks) != 3 || !bytes.Equal(current, []byte(bundle.Base)) || sourceDigest(current) != bundle.BaseDigest {
		return workflow.ErrConflict
	}
	var request struct {
		Environment string `json:"environment"`
	}
	if json.Unmarshal(bundle.Checks[0].Request, &request) != nil {
		return workflow.ErrInvalid
	}
	rebuilt, err := PrepareSource(bundle.Owner, bundle.Evidence.Authority, Record{Run: bundle.Evidence.RecordId, EnvironmentID: request.Environment, State: "candidate_ready", Candidate: bundle.Candidate, Digest: bundle.CandidateDigest, Events: bundle.Checks})
	if err != nil {
		return err
	}
	a, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	b, err := json.Marshal(rebuilt)
	if err != nil || !bytes.Equal(a, b) {
		return workflow.ErrConflict
	}
	return nil
}

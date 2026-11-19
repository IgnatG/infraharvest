// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

// The documents export writes. Each is built field by field from what the
// report and the inventory say (see build.go), never by marshalling them,
// so that nothing they hold reaches the output unless a field here names
// it. schemas/*.schema.json describes the same shapes, and Build checks
// every document against its schema before it returns it.

// The documents, by the name --schema takes.
const (
	DocInventory = "inventory"
	DocRun       = "run"
	DocGate      = "gate"
	DocGraph     = "graph"

	// DocDriftSummary is built from drift.json, not from the import's report.
	DocDriftSummary = "drift-summary"

	// DocOCSFInventory is the inventory as OCSF events (see ocsf.go).
	DocOCSFInventory = "ocsf-inventory"

	// DocOCSFFindings is the failed and skipped checks of the verification
	// gate as OCSF Detection Findings (see findings.go).
	DocOCSFFindings = "ocsf-findings"
)

// Documents are the documents export can write.
var Documents = []string{DocInventory, DocRun, DocGate, DocGraph, DocDriftSummary, DocOCSFInventory, DocOCSFFindings}

// The value of each document's schema field.
const (
	schemaInventory     = "infraharvest/inventory/v1"
	schemaRun           = "infraharvest/run/v1"
	schemaGate          = "infraharvest/gate/v1"
	schemaGraph         = "infraharvest/graph/v1"
	schemaDriftSummary  = "infraharvest/drift-summary/v1"
	schemaOCSFInventory = "infraharvest/ocsf-inventory/v1"
	schemaOCSFFindings  = "infraharvest/ocsf-findings/v1"
)

// hashAlg is the algorithm --hash-ids hashes with.
const hashAlg = "hmac-sha256"

type component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// counts are the report's CoverageTotal.
type counts struct {
	Discovered int `json:"discovered"`
	Imported   int `json:"imported"`
	LeftOut    int `json:"left_out"`
	Skipped    int `json:"skipped"`
	Excluded   int `json:"excluded"`
	Failed     int `json:"failed"`
	Managed    int `json:"managed"`
	OtherTool  int `json:"other_tool"`
	Cloud      int `json:"cloud"`
}

type typeCounts struct {
	Type   string `json:"type"`
	Counts counts `json:"counts"`
}

type scopeCounts struct {
	Scope  string `json:"scope"`
	Counts counts `json:"counts"`
}

type coverage struct {
	Totals counts        `json:"totals"`
	Types  []typeCounts  `json:"types"`
	Scopes []scopeCounts `json:"scopes"`
}

type resource struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Account     string            `json:"account"`
	Status      string            `json:"status"`
	StatusBasis string            `json:"status_basis"`
	Name        string            `json:"name,omitempty"`
	Region      string            `json:"region,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Owner       string            `json:"owner,omitempty"`
}

type redaction struct {
	TagKeysDropped []string `json:"tag_keys_dropped"`
	AllTagsDropped bool     `json:"all_tags_dropped,omitempty"`
	IDsHashed      bool     `json:"ids_hashed"`
	HashAlg        string   `json:"hash_alg"`
}

type inventoryDoc struct {
	Schema      string     `json:"schema"`
	RunID       string     `json:"run_id"`
	GeneratedAt string     `json:"generated_at"`
	Tool        component  `json:"tool"`
	Cloud       string     `json:"cloud"`
	Scopes      []string   `json:"scopes"`
	Resources   []resource `json:"resources"`
	Redaction   redaction  `json:"redaction"`
	Coverage    coverage   `json:"coverage"`
}

// The commands whose runs the run document describes. import and incremental
// (an import with --incremental, which adds to the roots the output directory
// has) leave the import's report in the output directory, and drift its own;
// discover leaves the inventories it saved there, and verify verify.json (see
// report.VerifyJSONFile).
const (
	CommandImport      = "import"
	CommandIncremental = "incremental"
	CommandDrift       = "drift"
	CommandDiscover    = "discover"
	CommandVerify      = "verify"
)

// Commands are the commands the run document can describe.
var Commands = []string{CommandImport, CommandIncremental, CommandDrift, CommandDiscover, CommandVerify}

// commandDocuments are the documents besides the run document that the run
// of a command has, if it has any: what discover saved is the OCSF
// inventory's resources, and verify's checks are the gate, and the findings
// of the gate's failed and skipped checks.
var commandDocuments = map[string][]string{
	CommandDiscover: {DocOCSFInventory},
	CommandVerify:   {DocGate, DocOCSFFindings},
}

// requestIDPattern is what a request ID looks like (see
// schemas/run-v1.schema.json).
const requestIDPattern = `^[A-Za-z0-9._:-]{1,128}$`

type runDoc struct {
	Schema       string    `json:"schema"`
	RunID        string    `json:"run_id"`
	RequestID    string    `json:"request_id,omitempty"`
	Command      string    `json:"command"`
	ExitCode     int       `json:"exit_code"`
	Outcome      string    `json:"outcome"`
	StartedAt    string    `json:"started_at,omitempty"`
	FinishedAt   string    `json:"finished_at,omitempty"`
	Tool         component `json:"tool"`
	Engine       component `json:"engine"`
	Coverage     counts    `json:"coverage"`
	FailureCodes []string  `json:"failure_codes,omitempty"`
}

// driftRunDoc is the run document of a run that records neither an exit
// code, which depends on flags, nor the versions of what ran, nor counts of
// what an import covers: a drift run (drift.json), and the runs of discover
// and verify.
type driftRunDoc struct {
	Schema       string   `json:"schema"`
	RunID        string   `json:"run_id"`
	RequestID    string   `json:"request_id,omitempty"`
	Command      string   `json:"command"`
	Outcome      string   `json:"outcome"`
	StartedAt    string   `json:"started_at,omitempty"`
	FinishedAt   string   `json:"finished_at,omitempty"`
	FailureCodes []string `json:"failure_codes,omitempty"`
}

type gateCheck struct {
	ID          string   `json:"id"`
	Passed      bool     `json:"passed"`
	Skipped     bool     `json:"skipped"`
	ReasonCodes []string `json:"reason_codes"`
}

type gateDoc struct {
	Schema string      `json:"schema"`
	RunID  string      `json:"run_id"`
	Passed bool        `json:"passed"`
	Checks []gateCheck `json:"checks"`
}

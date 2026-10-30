package task

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// A planning comment's first line names its document and, since #71, a fingerprint of
// the issue description it was written against: <!-- foreman:prd desc=0123456789ab -->.
// Comments posted before then carry only <!-- foreman:prd -->.
const (
	planMarkerOpen  = "<!-- foreman:"
	planMarkerClose = " -->"
	fingerprintKey  = "desc="
	fingerprintLen  = 12
)

// DescriptionFingerprint identifies an issue description, ignoring surrounding space.
func DescriptionFingerprint(description string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(description)))
	return hex.EncodeToString(sum[:])[:fingerprintLen]
}

// PlanMarker is the first line of doc's comment. An empty fingerprint writes the
// legacy form.
func PlanMarker(doc PlanDoc, fingerprint string) string {
	if fingerprint == "" {
		return planMarkerOpen + string(doc) + planMarkerClose
	}
	return planMarkerOpen + string(doc) + " " + fingerprintKey + fingerprint + planMarkerClose
}

// ParsePlanMarker reads the document and fingerprint from a comment that starts with a
// planning marker. Any other comment, including foreman's PR replies, is not a
// planning document.
func ParsePlanMarker(body string) (PlanDoc, string, bool) {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	inner, ok := strings.CutPrefix(strings.TrimSpace(line), planMarkerOpen)
	if !ok {
		return "", "", false
	}
	if inner, ok = strings.CutSuffix(inner, planMarkerClose); !ok {
		return "", "", false
	}
	fields := strings.Fields(inner)
	if len(fields) == 0 || len(fields) > 2 {
		return "", "", false
	}
	doc := PlanDoc(fields[0])
	if !doc.valid() {
		return "", "", false
	}
	if len(fields) == 1 {
		return doc, "", true
	}
	fp, ok := strings.CutPrefix(fields[1], fingerprintKey)
	if _, err := hex.DecodeString(fp); !ok || len(fp) != fingerprintLen || err != nil {
		return "", "", false
	}
	return doc, fp, true
}

func (d PlanDoc) valid() bool {
	// Read old analysis and design markers so they remain identifiable on issues.
	return d == Analysis || d == PRD || d == Design || d == ImplementationPlan
}

package proctest_test

import (
	"testing"

	"github.com/networkteam/sdd/internal/proctest"
	sdd "github.com/networkteam/sdd/pkg/application"
)

func TestCapture_PreflightTracksSelectedStagedAttachments(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		restaged       string
		wantPreflights int
		wantContent    string
	}{
		{name: "selected filename changed", restaged: "record.md", wantPreflights: 2, wantContent: "Revised record."},
		{name: "unrelated filename changed", restaged: "other.md", wantPreflights: 1, wantContent: "Original record."},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			world, session := newCaptureWorld(t, "staged-preflight")
			world.LLM.PreflightFindings = []proctest.PreflightFinding{{Severity: "high", Category: "test-block", Observation: "Review this finding."}}
			handle := session.Stage(t, "record.md", []byte("Original record."))
			serve := session.Start(t, "capture", nil)
			draft := captureDraft()
			draft["attachments"] = []any{handle}
			draft["body"] = "The observation has a [full record]({{attachments}}/record.md)."
			session.Report(t, serve.Instance, draft)
			serve = session.Answer(t, serve.Instance, "playback", "confirm", nil, "capture it")
			proctest.RequireStep(t, serve, "reviseOrOverride")

			session.Stage(t, scenario.restaged, []byte("Revised record."))
			session, _ = world.Resume(t, session.ID, "staged-preflight-resumed")
			world.LLM.PreflightFindings = nil
			serve = session.Answer(t, serve.Instance, "reviseOrOverride", "override", nil, "the finding does not apply")
			proctest.RequireStep(t, serve, "verifySummary")
			if got := world.LLM.Calls("preflight"); got != scenario.wantPreflights {
				t.Fatalf("preflight calls = %d, want %d", got, scenario.wantPreflights)
			}
			serve = session.Answer(t, serve.Instance, "verifySummary", "faithful", map[string]any{"fidelityNote": "matches"}, "")
			id, _ := serve.Produced["entryId"].(string)
			attachment, err := world.App.ReadAttachment(t.Context(), world.Identity, "proctest", sdd.ReadAttachmentRequest{EntryID: id, Filename: "record.md", MaxBytes: 1024})
			if err != nil || string(attachment.Page.Content) != scenario.wantContent {
				t.Fatalf("published attachment = %q, %v; want %q", string(attachment.Page.Content), err, scenario.wantContent)
			}
		})
	}
}

package workflowprobe

import (
	"net/http"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runPlanRevisionLifecycle() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	// Create the draft directly so the draft-stage detail can be inspected.
	var plan domain.RolloutPlan
	if err := harness.call(http.MethodPut, "/v1/plans", domain.PlanCreateInput{
		Zone:                "edge.example",
		MinimumSuccessRatio: 1,
		MaximumLatencyMS:    80,
		ExpectedNodes:       2,
	}, &plan); err != nil {
		return err
	}

	// Empty draft: only the draft timeline node, no revisions or assessments.
	draft, err := getPlanDetail(harness, plan.ID)
	if err != nil {
		return err
	}
	if draft.State != domain.PlanDraft || draft.ID != plan.ID {
		return ensure(false, "detail did not embed the main plan fields")
	}
	if draft.Zone != "edge.example" || draft.Version != 1 {
		return ensure(false, "detail lost main plan fields")
	}
	if len(draft.Timeline) != 1 || draft.Timeline[0].State != domain.PlanDraft {
		return ensure(false, "draft timeline should contain only the draft node")
	}
	if !draft.Timeline[0].At.Equal(plan.CreatedAt) {
		return ensure(false, "draft timeline node should be timestamped at creation")
	}
	if draft.RevisionSummary.Count != 0 || draft.RevisionSummary.LatestSerial != nil {
		return ensure(false, "draft revision summary should be empty")
	}
	if draft.AssessmentSummary.Count != 0 || draft.AssessmentSummary.Latest != nil {
		return ensure(false, "draft assessment summary should be empty")
	}

	var revision domain.ZoneRevision
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    2026092501,
		Authority: "ns1.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.10"},
			{Owner: "www.edge.example", Type: domain.EntryCNAME, Value: "edge.example"},
		},
	}, &revision); err != nil {
		return err
	}
	// Add a second revision so ordering and the latest serial are observable.
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    2026092502,
		Authority: "ns1.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.11"},
		},
	}, nil); err != nil {
		return err
	}
	withRevisions, err := getPlanDetail(harness, plan.ID)
	if err != nil {
		return err
	}
	if withRevisions.RevisionSummary.Count != 2 {
		return ensure(false, "revision count was not reported")
	}
	if withRevisions.RevisionSummary.LatestSerial == nil ||
		*withRevisions.RevisionSummary.LatestSerial != 2026092502 {
		return ensure(false, "latest revision serial was not reported")
	}
	if withRevisions.LatestRevisionID == "" || len(withRevisions.Timeline) != 1 {
		return ensure(false, "draft revision stage should still show only the draft node")
	}

	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/activate", map[string]any{
		"expected_version": withRevisions.Version,
	}, &plan); err != nil {
		return err
	}
	active, err := getPlanDetail(harness, plan.ID)
	if err != nil {
		return err
	}
	if active.State != domain.PlanActive || revision.Serial != 2026092501 {
		return ensure(false, "plan and revision were not activated")
	}
	if len(active.Timeline) != 2 ||
		active.Timeline[0].State != domain.PlanDraft ||
		active.Timeline[1].State != domain.PlanActive {
		return ensure(false, "active timeline should be ordered draft -> active")
	}
	if active.Timeline[1].At.IsZero() || active.ActivatedAt == nil ||
		!active.Timeline[1].At.Equal(*active.ActivatedAt) {
		return ensure(false, "active timeline node should match activated_at")
	}

	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    2026092503,
		Authority: "ns1.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.12"},
		},
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState)); err != nil {
		return err
	}

	var completed domain.RolloutPlan
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": plan.Version,
	}, &completed); err != nil {
		return err
	}
	if completed.State != domain.PlanCompleted {
		return ensure(false, "plan did not complete")
	}
	// A completed plan still returns the full summary in the same snapshot.
	finalDetail, err := getPlanDetail(harness, plan.ID)
	if err != nil {
		return err
	}
	if len(finalDetail.Timeline) != 3 ||
		finalDetail.Timeline[0].State != domain.PlanDraft ||
		finalDetail.Timeline[1].State != domain.PlanActive ||
		finalDetail.Timeline[2].State != domain.PlanCompleted {
		return ensure(false, "completed timeline should be ordered draft -> active -> completed")
	}
	if finalDetail.RevisionSummary.Count != 2 ||
		finalDetail.RevisionSummary.LatestSerial == nil ||
		*finalDetail.RevisionSummary.LatestSerial != 2026092502 {
		return ensure(false, "completed plan lost its revision summary")
	}
	if finalDetail.AssessmentSummary.Count != 0 || finalDetail.AssessmentSummary.Latest != nil {
		return ensure(false, "completed plan should expose an empty assessment summary")
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": completed.Version,
	}, nil)
	return expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState))
}

func getPlanDetail(harness *harness, planID string) (domain.PlanDetail, error) {
	var detail domain.PlanDetail
	err := harness.call(http.MethodGet, "/v1/plans/"+planID, nil, &detail)
	return detail, err
}

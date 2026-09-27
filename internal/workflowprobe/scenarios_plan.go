package workflowprobe

import (
	"net/http"
	"time"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runPlanRevisionLifecycle() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	var draft domain.RolloutPlan
	if err := harness.call(http.MethodPut, "/v1/plans", domain.PlanCreateInput{
		Zone:                "handover.example",
		MinimumSuccessRatio: 1,
		MaximumLatencyMS:    50,
		ExpectedNodes:       1,
	}, &draft); err != nil {
		return err
	}
	var draftView domain.PlanView
	if err := harness.call(http.MethodGet, "/v1/plans/"+draft.ID, nil, &draftView); err != nil {
		return err
	}
	if draftView.Zone != draft.Zone || draftView.Version != draft.Version {
		return ensure(false, "plan view does not carry the main plan fields")
	}
	if len(draftView.Summary.StateTimeline) != 1 ||
		draftView.Summary.StateTimeline[0].State != domain.PlanDraft ||
		!draftView.Summary.StateTimeline[0].EnteredAt.Equal(draft.CreatedAt) {
		return ensure(false, "draft plan summary does not show the draft state node")
	}
	if draftView.Summary.RevisionCount != 0 || draftView.Summary.LatestSerial != 0 ||
		draftView.Summary.AssessmentCount != 0 || draftView.Summary.LatestVerdict != "" {
		return ensure(false, "draft plan summary should have no revisions or assessments")
	}

	plan, revision, err := createActivePlan(
		harness,
		"edge.example",
		2026092501,
		2,
		1,
		80,
	)
	if err != nil {
		return err
	}
	if plan.State != domain.PlanActive || revision.Serial != 2026092501 {
		return ensure(false, "plan and revision were not activated")
	}

	var activeView domain.PlanView
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &activeView); err != nil {
		return err
	}
	if err := ensureTimeline(activeView.Summary.StateTimeline, domain.PlanDraft, domain.PlanActive); err != nil {
		return err
	}
	if activeView.Summary.RevisionCount != 1 ||
		activeView.Summary.LatestRevisionID != revision.ID ||
		activeView.Summary.LatestSerial != revision.Serial {
		return ensure(false, "active plan summary does not reflect the latest revision")
	}
	if activeView.Summary.AssessmentCount != 0 || activeView.Summary.LatestVerdict != "" {
		return ensure(false, "active plan summary should have no assessments")
	}

	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/revisions", domain.RevisionCreateInput{
		Serial:    2026092502,
		Authority: "ns1.edge.example",
		Entries: []domain.ZoneEntry{
			{Owner: "edge.example", Type: domain.EntryA, Value: "192.0.2.11"},
		},
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState)); err != nil {
		return err
	}

	harness.clock.Advance(time.Minute)
	var completed domain.RolloutPlan
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": plan.Version,
	}, &completed); err != nil {
		return err
	}
	if completed.State != domain.PlanCompleted {
		return ensure(false, "plan did not complete")
	}
	var completedView domain.PlanView
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &completedView); err != nil {
		return err
	}
	if completedView.Version != completed.Version {
		return ensure(false, "completed plan view mixed a stale plan version")
	}
	if err := ensureTimeline(
		completedView.Summary.StateTimeline,
		domain.PlanDraft,
		domain.PlanActive,
		domain.PlanCompleted,
	); err != nil {
		return err
	}
	if completedView.Summary.RevisionCount != 1 ||
		completedView.Summary.LatestSerial != revision.Serial {
		return ensure(false, "completed plan summary lost the revision overview")
	}

	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": completed.Version,
	}, nil)
	return expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState))
}

func ensureTimeline(timeline []domain.PlanStateNode, states ...domain.PlanState) error {
	if len(timeline) != len(states) {
		return ensure(false, "state timeline does not list every entered state")
	}
	for index, state := range states {
		if timeline[index].State != state {
			return ensure(false, "state timeline is not ordered by transition time")
		}
		if timeline[index].EnteredAt.IsZero() {
			return ensure(false, "state timeline node is missing its timestamp")
		}
		if index > 0 && timeline[index].EnteredAt.Before(timeline[index-1].EnteredAt) {
			return ensure(false, "state timeline timestamps are not chronological")
		}
	}
	return nil
}

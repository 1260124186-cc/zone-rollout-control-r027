package workflowprobe

import (
	"fmt"
	"net/http"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runConsistencyAssessment() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	if _, err := enrollNode(harness, "resolver-assess-01", "west"); err != nil {
		return err
	}
	if _, err := enrollNode(harness, "resolver-assess-02", "west"); err != nil {
		return err
	}
	plan, revision, err := createActivePlan(harness, "assess.example", 3001, 2, 1, 40)
	if err != nil {
		return err
	}
	group, err := allocate(harness, plan.ID, "assess-lease-0001", "west", 2)
	if err != nil {
		return err
	}
	for index, leaseID := range group.LeaseIDs {
		requestKey := fmt.Sprintf("assess-probe-%s", leaseID)
		if _, err := submitProbe(harness, leaseID, requestKey, true, revision.Serial, 10+index); err != nil {
			return err
		}
	}

	var consistent domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "assess-request-0001",
	}, &consistent); err != nil {
		return err
	}
	if consistent.Verdict != domain.VerdictConsistent || consistent.ProbeCount != 2 {
		return ensure(false, "consistent assessment did not include both probes")
	}
	var replay domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "assess-request-0001",
	}, &replay); err != nil {
		return err
	}
	if replay.ID != consistent.ID {
		return ensure(false, "assessment replay returned another result")
	}
	var latest domain.RolloutAssessment
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID+"/assessments/latest", nil, &latest); err != nil {
		return err
	}
	if latest.ID != consistent.ID {
		return ensure(false, "latest assessment does not match")
	}

	// The plan detail snapshot must carry the assessment count and latest step.
	var assessed domain.PlanDetail
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &assessed); err != nil {
		return err
	}
	if assessed.AssessmentSummary.Count != 1 || assessed.AssessmentSummary.Latest == nil {
		return ensure(false, "plan detail did not summarize the assessment")
	}
	if assessed.AssessmentSummary.Latest.ID != consistent.ID ||
		assessed.AssessmentSummary.Latest.Verdict != domain.VerdictConsistent {
		return ensure(false, "plan detail latest assessment result is wrong")
	}
	if assessed.RevisionSummary.Count != 1 ||
		assessed.RevisionSummary.LatestSerial == nil ||
		*assessed.RevisionSummary.LatestSerial != revision.Serial {
		return ensure(false, "plan detail revision summary is wrong after assessment")
	}
	if len(assessed.Timeline) != 2 || assessed.Timeline[1].State != domain.PlanActive {
		return ensure(false, "plan detail timeline is wrong for an active assessed plan")
	}

	// Completing the plan must not hide the summary.
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": assessed.Version,
	}, nil); err != nil {
		return err
	}
	completedDetail, err := getPlanDetail(harness, plan.ID)
	if err != nil {
		return err
	}
	if completedDetail.State != domain.PlanCompleted ||
		completedDetail.AssessmentSummary.Count != 1 ||
		completedDetail.AssessmentSummary.Latest == nil ||
		completedDetail.AssessmentSummary.Latest.ID != consistent.ID {
		return ensure(false, "completed plan could not be read with its full summary")
	}

	// Dangling references must surface as storage_failure rather than look empty.
	missingRevisionID := consistent.RevisionID
	if err := harness.restartWithState(func(state *domain.State) {
		delete(state.Revisions, missingRevisionID)
	}); err != nil {
		return err
	}
	err = harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, nil)
	if err := expectFailure(err, http.StatusInternalServerError, string(domain.CodeStorageFailure)); err != nil {
		return err
	}

	// Restore the revision, then break the assessment reference instead.
	if err := harness.restartWithState(func(state *domain.State) {
		state.Revisions[missingRevisionID] = revision
		planRecord := state.Plans[plan.ID]
		planRecord.LatestAssessmentID = "asm_does_not_exist"
		state.Plans[plan.ID] = planRecord
	}); err != nil {
		return err
	}
	err = harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, nil)
	if err := expectFailure(err, http.StatusInternalServerError, string(domain.CodeStorageFailure)); err != nil {
		return err
	}

	reviewPlan, reviewRevision, err := createActivePlan(harness, "review.example", 4001, 1, 1, 20)
	if err != nil {
		return err
	}
	reviewGroup, err := allocate(harness, reviewPlan.ID, "review-lease-0001", "west", 1)
	if err != nil {
		return err
	}
	if _, err := submitProbe(
		harness,
		reviewGroup.LeaseIDs[0],
		"review-probe-0001",
		true,
		reviewRevision.Serial+1,
		30,
	); err != nil {
		return err
	}
	var review domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+reviewPlan.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "review-request-0001",
	}, &review); err != nil {
		return err
	}
	if review.Verdict != domain.VerdictReviewRequired {
		return ensure(false, "mismatched serial did not require review")
	}
	return nil
}

package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"
)

func ActivatePlan(plan *RolloutPlan, now time.Time) error {
	if plan.State != PlanDraft {
		return InvalidState("plan can be activated only from draft", map[string]any{
			"plan_id": plan.ID,
			"state":   plan.State,
		})
	}
	if plan.LatestRevisionID == "" {
		return InvalidState("plan needs a zone revision before activation", map[string]any{
			"plan_id": plan.ID,
		})
	}
	plan.State = PlanActive
	plan.ActivatedAt = timePointer(now.UTC())
	plan.Version++
	return nil
}

func CompletePlan(plan *RolloutPlan, now time.Time) error {
	if plan.State != PlanActive {
		return InvalidState("plan can be completed only from active", map[string]any{
			"plan_id": plan.ID,
			"state":   plan.State,
		})
	}
	plan.State = PlanCompleted
	plan.CompletedAt = timePointer(now.UTC())
	plan.Version++
	return nil
}

func LeaseFingerprint(planID string, input LeaseInput) string {
	payload := fmt.Sprintf(
		"%s|%s|%s|%d|%d",
		planID,
		input.RequestKey,
		input.Region,
		input.LeaseSeconds,
		input.MaxNodes,
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func ProbeFingerprint(leaseID string, input ProbeInput) string {
	payload := fmt.Sprintf(
		"%s|%s|%t|%d|%d|%s",
		leaseID,
		input.RequestKey,
		input.Success,
		input.ObservedSerial,
		input.LatencyMS,
		input.ObservedAt.UTC().Format(time.RFC3339Nano),
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func BuildAssessment(
	id string,
	plan RolloutPlan,
	revision ZoneRevision,
	probes []Probe,
	now time.Time,
) RolloutAssessment {
	reasons := make(map[string]struct{})
	if len(probes) == 0 {
		reasons["no_probes"] = struct{}{}
	}
	probeCount := len(probes)
	successCount := 0
	maximumLatency := 0
	nodes := make(map[string]struct{})
	for _, probe := range probes {
		nodes[probe.NodeID] = struct{}{}
		if probe.LatencyMS > maximumLatency {
			maximumLatency = probe.LatencyMS
		}
		if probe.Success {
			successCount++
			if probe.ObservedSerial != revision.Serial {
				reasons["serial_mismatch"] = struct{}{}
			}
		}
	}
	if len(nodes) < plan.ExpectedNodes {
		reasons["node_not_observed"] = struct{}{}
	}
	successRatio := 0.0
	if probeCount > 0 {
		successRatio = round(float64(successCount)/float64(probeCount), 6)
	}
	if successRatio < plan.MinimumSuccessRatio {
		reasons["success_ratio_below_minimum"] = struct{}{}
	}
	if maximumLatency > plan.MaximumLatencyMS {
		reasons["latency_limit_exceeded"] = struct{}{}
	}
	reasonList := make([]string, 0, len(reasons))
	for reason := range reasons {
		reasonList = append(reasonList, reason)
	}
	sort.Strings(reasonList)
	verdict := VerdictConsistent
	if len(reasonList) > 0 {
		verdict = VerdictReviewRequired
	}
	return RolloutAssessment{
		ID:               id,
		PlanID:           plan.ID,
		RevisionID:       revision.ID,
		Verdict:          verdict,
		Reasons:          reasonList,
		ProbeCount:       probeCount,
		SuccessCount:     successCount,
		SuccessRatio:     successRatio,
		MaximumLatencyMS: maximumLatency,
		CreatedAt:        now.UTC(),
	}
}

func BuildPlanSummary(plan RolloutPlan, state *State) (PlanSummary, error) {
	timeline := make([]PlanStateNode, 0, 3)
	timeline = append(timeline, PlanStateNode{State: PlanDraft, EnteredAt: plan.CreatedAt.UTC()})
	if plan.ActivatedAt != nil {
		timeline = append(timeline, PlanStateNode{State: PlanActive, EnteredAt: plan.ActivatedAt.UTC()})
	}
	if plan.CompletedAt != nil {
		timeline = append(timeline, PlanStateNode{State: PlanCompleted, EnteredAt: plan.CompletedAt.UTC()})
	}
	sort.SliceStable(timeline, func(i, j int) bool {
		return timeline[i].EnteredAt.Before(timeline[j].EnteredAt)
	})
	summary := PlanSummary{StateTimeline: timeline}
	for _, revision := range state.Revisions {
		if revision.PlanID == plan.ID {
			summary.RevisionCount++
		}
	}
	if plan.LatestRevisionID != "" {
		revision, ok := state.Revisions[plan.LatestRevisionID]
		if !ok {
			return PlanSummary{}, NewError(CodeStorageFailure, "plan references a missing revision", map[string]any{
				"plan_id":     plan.ID,
				"revision_id": plan.LatestRevisionID,
			})
		}
		summary.LatestRevisionID = revision.ID
		summary.LatestSerial = revision.Serial
	} else if summary.RevisionCount > 0 {
		return PlanSummary{}, NewError(CodeStorageFailure, "plan has revisions but no latest revision reference", map[string]any{
			"plan_id": plan.ID,
		})
	}
	for _, assessment := range state.Assessments {
		if assessment.PlanID == plan.ID {
			summary.AssessmentCount++
		}
	}
	if plan.LatestAssessmentID != "" {
		assessment, ok := state.Assessments[plan.LatestAssessmentID]
		if !ok {
			return PlanSummary{}, NewError(CodeStorageFailure, "plan references a missing assessment", map[string]any{
				"plan_id":       plan.ID,
				"assessment_id": plan.LatestAssessmentID,
			})
		}
		summary.LatestAssessmentID = assessment.ID
		summary.LatestVerdict = assessment.Verdict
	} else if summary.AssessmentCount > 0 {
		return PlanSummary{}, NewError(CodeStorageFailure, "plan has assessments but no latest assessment reference", map[string]any{
			"plan_id": plan.ID,
		})
	}
	return summary, nil
}

func round(value float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(value*scale) / scale
}

func timePointer(value time.Time) *time.Time {
	return &value
}

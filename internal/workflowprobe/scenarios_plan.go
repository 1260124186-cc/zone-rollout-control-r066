package workflowprobe

import (
	"fmt"
	"net/http"

	"solo-0014-zone-rollout-control/internal/domain"
)

func runPlanRevisionLifecycle() error {
	harness, err := newHarness()
	if err != nil {
		return err
	}
	defer harness.close()

	if _, err := enrollNode(harness, "resolver-plan-01", "east"); err != nil {
		return err
	}
	if _, err := enrollNode(harness, "resolver-plan-02", "east"); err != nil {
		return err
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

	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": plan.Version,
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeAssessmentRequired)); err != nil {
		return err
	}

	group, err := allocate(harness, plan.ID, "plan-lease-0001", "east", 2)
	if err != nil {
		return err
	}
	for index, leaseID := range group.LeaseIDs {
		requestKey := fmt.Sprintf("plan-probe-%s", leaseID)
		if _, err := submitProbe(harness, leaseID, requestKey, true, revision.Serial, 10+index); err != nil {
			return err
		}
	}
	var assessment domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "plan-assess-0001",
	}, &assessment); err != nil {
		return err
	}
	if assessment.Verdict != domain.VerdictConsistent {
		return ensure(false, "qualifying probes did not conclude consistent")
	}

	var current domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &current); err != nil {
		return err
	}
	var completed domain.RolloutPlan
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": current.Version,
	}, &completed); err != nil {
		return err
	}
	if completed.State != domain.PlanCompleted {
		return ensure(false, "plan did not complete")
	}
	var reread domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &reread); err != nil {
		return err
	}
	if reread.State != domain.PlanCompleted || reread.LatestAssessmentID != assessment.ID {
		return ensure(false, "completed plan readback changed")
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": completed.Version,
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState)); err != nil {
		return err
	}

	blocked, blockedRevision, err := createActivePlan(harness, "blocked.example", 2026092601, 1, 1, 40)
	if err != nil {
		return err
	}
	blockedGroup, err := allocate(harness, blocked.ID, "blocked-lease-0001", "east", 1)
	if err != nil {
		return err
	}
	if _, err := submitProbe(
		harness,
		blockedGroup.LeaseIDs[0],
		"blocked-probe-0001",
		true,
		blockedRevision.Serial+1,
		10,
	); err != nil {
		return err
	}
	var review domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+blocked.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "blocked-assess-0001",
	}, &review); err != nil {
		return err
	}
	if review.Verdict != domain.VerdictReviewRequired {
		return ensure(false, "mismatched serial did not require review")
	}
	var blockedCurrent domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+blocked.ID, nil, &blockedCurrent); err != nil {
		return err
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+blocked.ID+"/complete", map[string]any{
		"expected_version": blockedCurrent.Version,
	}, nil)
	return expectFailure(err, http.StatusConflict, string(domain.CodeAssessmentRejected))
}

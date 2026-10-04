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

	if _, err := enrollNode(harness, "resolver-life-01", "north"); err != nil {
		return err
	}
	if _, err := enrollNode(harness, "resolver-life-02", "north"); err != nil {
		return err
	}
	plan, revision, err := createActivePlan(
		harness,
		"life.example",
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

	// Completing without any assessment is now rejected with a distinct reason.
	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": plan.Version,
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeCompletionNotAssessed)); err != nil {
		return err
	}

	// A review_required assessment keeps the plan uncompletable, with its own
	// reason distinct from the missing-assessment case.
	if _, err := enrollNode(harness, "resolver-life-review-01", "south"); err != nil {
		return err
	}
	reviewPlan, reviewRevision, err := createActivePlan(harness, "life-review.example", 6001, 1, 1, 20)
	if err != nil {
		return err
	}
	reviewGroup, err := allocate(harness, reviewPlan.ID, "life-review-lease-0001", "south", 1)
	if err != nil {
		return err
	}
	if _, err := submitProbe(
		harness,
		reviewGroup.LeaseIDs[0],
		"life-review-probe-0001",
		true,
		reviewRevision.Serial+1,
		10,
	); err != nil {
		return err
	}
	var review domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+reviewPlan.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "life-review-assess-0001",
	}, &review); err != nil {
		return err
	}
	if review.Verdict != domain.VerdictReviewRequired {
		return ensure(false, "review plan assessment was expected to require review")
	}
	var refreshedReview domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+reviewPlan.ID, nil, &refreshedReview); err != nil {
		return err
	}
	err = harness.call(http.MethodPatch, "/v1/plans/"+reviewPlan.ID+"/complete", map[string]any{
		"expected_version": refreshedReview.Version,
	}, nil)
	if err := expectFailure(err, http.StatusConflict, string(domain.CodeCompletionNotConsistent)); err != nil {
		return err
	}

	// A consistent assessment against the current revision unlocks completion.
	group, err := allocate(harness, plan.ID, "life-lease-0001", "north", 2)
	if err != nil {
		return err
	}
	for index, leaseID := range group.LeaseIDs {
		if _, err := submitProbe(
			harness,
			leaseID,
			fmt.Sprintf("life-probe-%s", leaseID),
			true,
			revision.Serial,
			10+index,
		); err != nil {
			return err
		}
	}
	var assessed domain.RolloutAssessment
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/assessments", domain.AssessmentInput{
		RequestKey: "life-assess-0001",
	}, &assessed); err != nil {
		return err
	}
	if assessed.Verdict != domain.VerdictConsistent {
		return ensure(false, "plan assessment was expected to be consistent")
	}
	var refreshed domain.RolloutPlan
	if err := harness.call(http.MethodGet, "/v1/plans/"+plan.ID, nil, &refreshed); err != nil {
		return err
	}

	var completed domain.RolloutPlan
	if err := harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": refreshed.Version,
	}, &completed); err != nil {
		return err
	}
	if completed.State != domain.PlanCompleted {
		return ensure(false, "plan did not complete")
	}
	// Repeating completion on an ended plan remains the same idempotent conflict.
	err = harness.call(http.MethodPatch, "/v1/plans/"+plan.ID+"/complete", map[string]any{
		"expected_version": completed.Version,
	}, nil)
	return expectFailure(err, http.StatusConflict, string(domain.CodeInvalidState))
}

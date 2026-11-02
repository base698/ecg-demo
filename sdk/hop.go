package sdk

import (
	"context"
	"fmt"

	"github.com/base698/ecg-demo/ecg"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// Hop annotates an existing server span for ECG's linear trace adapter.
// NodeKey must come from the application's registered release identity.
// Scenario and Flags are bounded, application-owned labels; do not use bodies,
// customer IDs or credentials. Baggage is propagation, not an auth mechanism.
type Hop struct {
	Service, Version, Instance, NodeKey, Scenario, Flags, Tenant, Intended string
	Eligible                                                               bool
}
type Prefix struct{ In, Out string }

func AnnotateHop(ctx context.Context, h Hop) (context.Context, Prefix, error) {
	if h.Service == "" || h.Version == "" || h.Instance == "" || h.NodeKey == "" || h.Scenario == "" || len(h.Scenario) > 128 || len(h.Flags) > 4096 {
		return ctx, Prefix{}, fmt.Errorf("invalid hop identity or execution labels")
	}
	bag := baggage.FromContext(ctx)
	p := Prefix{In: bag.Member(ecg.BagPrefix).Value()}
	p.Out = ecg.Chain(p.In, h.NodeKey)
	trace.SpanFromContext(ctx).SetAttributes(attribute.String(ecg.AttrService, h.Service), attribute.String(ecg.AttrVersion, h.Version), attribute.String(ecg.AttrInstance, h.Instance), attribute.String(ecg.AttrNodeKey, h.NodeKey), attribute.String(ecg.AttrPrefixIn, p.In), attribute.String(ecg.AttrPrefixOut, p.Out), attribute.String(ecg.AttrScenario, h.Scenario), attribute.String(ecg.AttrFlags, h.Flags), attribute.String(ecg.AttrTenant, h.Tenant), attribute.Bool(ecg.AttrEligible, h.Eligible), attribute.String(ecg.AttrIntended, h.Intended))
	for k, v := range map[string]string{ecg.BagPrefix: p.Out, ecg.BagScenario: h.Scenario, ecg.BagFlags: h.Flags, ecg.BagTenant: h.Tenant} {
		m, err := baggage.NewMember(k, v)
		if err != nil {
			return ctx, Prefix{}, err
		}
		bag, err = bag.SetMember(m)
		if err != nil {
			return ctx, Prefix{}, err
		}
	}
	return baggage.ContextWithBaggage(ctx, bag), p, nil
}

// AnnotateCheck supports a single versioned aggregate check on a server span.
// Applications needing multiple checks should use Operation.Check instead.
func AnnotateCheck(ctx context.Context, versionedID, status, reason string) error {
	if versionedID == "" || len(versionedID) > 128 || len(reason) > 512 || (status != "passed" && status != "failed" && status != "unknown") {
		return fmt.Errorf("invalid aggregate check")
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String(ecg.AttrOracle, versionedID), attribute.String(ecg.AttrAssertion, status), attribute.String(ecg.AttrAssertionReason, reason))
	return nil
}

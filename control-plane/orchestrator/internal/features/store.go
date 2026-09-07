package features

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// Store retrieves feature values for one evaluation.
type Store interface {
	// Fetch returns one Feature per Request, in the same order. It reports an
	// error only when the store itself could not answer; a feature that simply
	// has no value is a successful fetch with ABSENT freshness.
	Fetch(ctx context.Context, requests []Request) ([]*riskv1.Feature, error)
}

// StalenessBudget is how old a value may be and still count as fresh.
//
// Beyond it a value is STALE rather than discarded: an old count is usually
// better evidence than no count, and the agent is told which it received.
const StalenessBudget = 30 * time.Second

// RedisStore reads features from Redis or a protocol-compatible alternative.
type RedisStore struct {
	client redis.Cmdable
	now    func() time.Time
}

// NewRedisStore wraps an established client.
func NewRedisStore(client redis.Cmdable) *RedisStore {
	return &RedisStore{client: client, now: time.Now}
}

// Fetch retrieves every requested feature in a single round trip.
//
// One MGET rather than one call per feature: the whole batch has a budget of
// roughly 12 ms including network, which a dozen sequential round trips would
// spend on latency alone.
func (s *RedisStore) Fetch(ctx context.Context, requests []Request) ([]*riskv1.Feature, error) {
	if len(requests) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(requests))
	for _, request := range requests {
		keys = append(keys, key(request))
	}

	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("features: store did not answer: %w", err)
	}

	out := make([]*riskv1.Feature, 0, len(requests))
	for i, request := range requests {
		var raw any
		if i < len(values) {
			raw = values[i]
		}
		out = append(out, s.decode(request, raw))
	}
	return out, nil
}

// key is `f:{scope}:{subject}:{name}:{window}`. The window is part of the key
// rather than the name so that names stay comparable across windows.
func key(request Request) string {
	return strings.Join([]string{
		"f",
		string(request.Scope),
		request.Subject,
		request.Name,
		strconv.Itoa(int(request.Window.Number())),
	}, ":")
}

// decode turns a stored `value|unixSeconds|definitionVersion` triple into a
// Feature, classifying its freshness.
//
// A missing key is ABSENT: the store answered, and there is genuinely nothing
// there. A malformed value is UNAVAILABLE, because a value nobody can parse is
// not a value, and guessing zero would invent evidence.
func (s *RedisStore) decode(request Request, raw any) *riskv1.Feature {
	feature := &riskv1.Feature{
		Name:              request.Name,
		Window:            request.Window,
		DefinitionVersion: CatalogueVersion,
	}

	if raw == nil {
		feature.Freshness = riskv1.FeatureFreshness_FEATURE_FRESHNESS_ABSENT
		return feature
	}

	encoded, ok := raw.(string)
	if !ok {
		feature.Freshness = riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNAVAILABLE
		return feature
	}

	count, computedAt, version, err := parse(encoded)
	if err != nil {
		feature.Freshness = riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNAVAILABLE
		return feature
	}

	if version != "" {
		feature.DefinitionVersion = version
	}
	feature.Value = &riskv1.FeatureValue{Kind: &riskv1.FeatureValue_Count{Count: count}}
	feature.ComputedAt = timestamppb.New(computedAt)

	if s.now().Sub(computedAt) > StalenessBudget {
		feature.Freshness = riskv1.FeatureFreshness_FEATURE_FRESHNESS_STALE
	} else {
		feature.Freshness = riskv1.FeatureFreshness_FEATURE_FRESHNESS_FRESH
	}
	return feature
}

func parse(encoded string) (int64, time.Time, string, error) {
	parts := strings.Split(encoded, "|")
	if len(parts) < 2 {
		return 0, time.Time{}, "", fmt.Errorf("features: malformed value")
	}

	count, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, time.Time{}, "", fmt.Errorf("features: malformed count: %w", err)
	}

	seconds, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, time.Time{}, "", fmt.Errorf("features: malformed timestamp: %w", err)
	}

	version := ""
	if len(parts) > 2 {
		version = parts[2]
	}
	return count, time.Unix(seconds, 0), version, nil
}

// Encode renders a value in the store's format. It exists so that seeding a
// store in a test or a local environment cannot drift from how it is read.
func Encode(count int64, computedAt time.Time, definitionVersion string) string {
	return fmt.Sprintf("%d|%d|%s", count, computedAt.Unix(), definitionVersion)
}

// Key exposes the key layout for the same reason.
func Key(request Request) string {
	return key(request)
}

// Unavailable renders every requested feature as unknown.
//
// Used when the store could not answer at all. Every feature must still be
// reported, so that an outage reaches agents as "I do not know" rather than as
// a silently shorter feature set.
func Unavailable(requests []Request) []*riskv1.Feature {
	out := make([]*riskv1.Feature, 0, len(requests))
	for _, request := range requests {
		out = append(out, &riskv1.Feature{
			Name:              request.Name,
			Window:            request.Window,
			Freshness:         riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNAVAILABLE,
			DefinitionVersion: CatalogueVersion,
		})
	}
	return out
}

package pullrequest

import "github.com/helixml/kodit/domain/repository"

// WithNumber filters by the "number" column.
func WithNumber(number int) repository.Option {
	return repository.WithCondition("number", number)
}

// WithEnrichmentIDIn filters by the "enrichment_id" column using IN.
func WithEnrichmentIDIn(ids []int64) repository.Option {
	return repository.WithConditionIn("enrichment_id", ids)
}
